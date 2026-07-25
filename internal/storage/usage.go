package storage

import (
	"context"
	"fmt"
	"time"
)

// UsageEventRow is one persisted usage_events row (spec §31, §6.1, §14.4). It is
// the durable substrate behind the in-process quality.Accounting aggregation.
type UsageEventRow struct {
	ID                int64
	TaskID            string
	ProjectID         string
	Provider          string
	Model             string
	Kind              string // "coding" | "image"
	InputTokens       int
	CachedInputTokens int
	OutputTokens      int
	Generations       int
	CostUSD           float64
	OccurredAt        time.Time
}

// RecordUsageEvent inserts one usage event and returns its assigned id. The
// timestamp is defaulted to now (UTC) when zero.
func (d *DB) RecordUsageEvent(ctx context.Context, r UsageEventRow) (int64, error) {
	if r.OccurredAt.IsZero() {
		r.OccurredAt = time.Now().UTC()
	}
	res, err := d.db.ExecContext(ctx, `
INSERT INTO usage_events
	(task_id, project_id, provider, model, kind,
	 input_tokens, cached_input_tokens, output_tokens, generations, cost_usd, occurred_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.TaskID, r.ProjectID, r.Provider, r.Model, r.Kind,
		r.InputTokens, r.CachedInputTokens, r.OutputTokens, r.Generations, r.CostUSD,
		r.OccurredAt.Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("storage: record usage event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("storage: usage event last insert id: %w", err)
	}
	return id, nil
}

// UsageFilter narrows a [DB.ListUsageEvents] query. Zero values mean "no
// constraint" except Limit, where 0 means "default limit".
type UsageFilter struct {
	ProjectID string
	TaskID    string
	Provider  string
	Limit     int
}

// ListUsageEvents returns usage rows matching the filter, oldest first.
func (d *DB) ListUsageEvents(ctx context.Context, f UsageFilter) ([]UsageEventRow, error) {
	q := `SELECT id, task_id, project_id, provider, model, kind,
		input_tokens, cached_input_tokens, output_tokens, generations, cost_usd, occurred_at
	FROM usage_events WHERE 1=1`
	var args []any
	if f.ProjectID != "" {
		q += ` AND project_id = ?`
		args = append(args, f.ProjectID)
	}
	if f.TaskID != "" {
		q += ` AND task_id = ?`
		args = append(args, f.TaskID)
	}
	if f.Provider != "" {
		q += ` AND provider = ?`
		args = append(args, f.Provider)
	}
	q += ` ORDER BY id ASC`
	limit := f.Limit
	if limit <= 0 {
		limit = 10000
	}
	q += ` LIMIT ?`
	args = append(args, limit)

	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: query usage events: %w", err)
	}
	defer rows.Close()
	var out []UsageEventRow
	for rows.Next() {
		var r UsageEventRow
		var ts string
		if err := rows.Scan(&r.ID, &r.TaskID, &r.ProjectID, &r.Provider, &r.Model, &r.Kind,
			&r.InputTokens, &r.CachedInputTokens, &r.OutputTokens, &r.Generations, &r.CostUSD, &ts); err != nil {
			return nil, fmt.Errorf("storage: scan usage event: %w", err)
		}
		r.OccurredAt, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterate usage events: %w", err)
	}
	return out, nil
}

// CountUsageEvents returns the total number of usage rows.
func (d *DB) CountUsageEvents(ctx context.Context) (int64, error) {
	var n int64
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_events`).Scan(&n); err != nil {
		return 0, fmt.Errorf("storage: count usage events: %w", err)
	}
	return n, nil
}
