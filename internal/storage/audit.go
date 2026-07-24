package storage

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// AuditEvent is a single persisted audit row (spec §29.4, AC-30). It is a
// data-only struct that mirrors the audit_events table; the audit package adds
// the domain typing on top.
type AuditEvent struct {
	ID        int64  // monotonic sequence number (primary key)
	Timestamp string // RFC3339Nano, UTC
	Scope     string // "system" | "project" | "task"
	ScopeID   string // "global" | project id | task id
	Type      string // e.g. "daemon.started"
	Actor     string // "user" | "daemon" | "system"
	Payload   string // raw JSON object
}

// AuditFilter narrows a [DB.ListAuditEvents] query. Zero values mean
// "no constraint" except Limit, where 0 means "default limit".
type AuditFilter struct {
	Scope       string // optional: match exact scope
	ScopeID     string // optional: match exact scope id
	Type        string // optional: match exact event type
	AfterID     int64  // optional: return rows with id > AfterID
	Limit       int    // optional; 0 -> 1000
	NewestFirst bool   // optional: order newest first instead of chronological
}

const defaultAuditLimit = 1000

// AppendAuditEvent inserts one audit row, returning its assigned id. The
// timestamp is set to now (UTC, RFC3339Nano) if e.Timestamp is empty.
func (d *DB) AppendAuditEvent(ctx context.Context, e AuditEvent) (int64, error) {
	return appendAuditEvent(ctx, d.db, e)
}

// AppendAuditEvent inserts one audit row as part of tx, so an audit event can
// share the same SQLite transaction as the state mutation it records (spec
// §11.4, ADR-0003).
func (t *Tx) AppendAuditEvent(ctx context.Context, e AuditEvent) (int64, error) {
	return appendAuditEvent(ctx, t.tx, e)
}

func appendAuditEvent(ctx context.Context, e executor, ev AuditEvent) (int64, error) {
	if strings.TrimSpace(ev.Timestamp) == "" {
		ev.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	res, err := e.ExecContext(ctx, `
INSERT INTO audit_events (ts, scope, scope_id, event_type, actor, payload)
VALUES (?, ?, ?, ?, ?, ?)`,
		ev.Timestamp, ev.Scope, ev.ScopeID, ev.Type, ev.Actor, ev.Payload)
	if err != nil {
		return 0, fmt.Errorf("storage: append audit event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("storage: audit event last insert id: %w", err)
	}
	return id, nil
}

// ListAuditEvents returns audit rows matching filter, ordered chronologically
// (unless filter.NewestFirst).
func (d *DB) ListAuditEvents(ctx context.Context, f AuditFilter) ([]AuditEvent, error) {
	var (
		sb   strings.Builder
		args []any
	)
	sb.WriteString(`SELECT id, ts, scope, scope_id, event_type, actor, payload FROM audit_events WHERE 1=1`)
	if f.Scope != "" {
		sb.WriteString(` AND scope = ?`)
		args = append(args, f.Scope)
	}
	if f.ScopeID != "" {
		sb.WriteString(` AND scope_id = ?`)
		args = append(args, f.ScopeID)
	}
	if f.Type != "" {
		sb.WriteString(` AND event_type = ?`)
		args = append(args, f.Type)
	}
	if f.AfterID > 0 {
		sb.WriteString(` AND id > ?`)
		args = append(args, f.AfterID)
	}
	if f.NewestFirst {
		sb.WriteString(` ORDER BY id DESC`)
	} else {
		sb.WriteString(` ORDER BY id ASC`)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = defaultAuditLimit
	}
	sb.WriteString(` LIMIT ?`)
	args = append(args, limit)

	rows, err := d.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("storage: query audit events: %w", err)
	}
	defer rows.Close()

	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.Scope, &e.ScopeID, &e.Type, &e.Actor, &e.Payload); err != nil {
			return nil, fmt.Errorf("storage: scan audit event: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterate audit events: %w", err)
	}
	return out, nil
}

// CountAuditEvents returns the total number of audit rows, useful for tests and
// health checks.
func (d *DB) CountAuditEvents(ctx context.Context) (int64, error) {
	var n int64
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events`).Scan(&n); err != nil {
		// Rows may not exist yet if migrations were not run; surface a clear
		// error so callers can distinguish "empty" from "not migrated".
		return 0, fmt.Errorf("storage: count audit events: %w", err)
	}
	return n, nil
}

// AssertAppendOnly reports whether the audit_events table rejects UPDATE and
// DELETE (it should, via triggers). It returns an error if mutation succeeds.
func (d *DB) AssertAppendOnly(ctx context.Context) error {
	if _, err := d.db.ExecContext(ctx, `INSERT INTO audit_events (ts, scope, scope_id, event_type, actor, payload) VALUES ('1970-01-01T00:00:00Z','system','probe','probe','system','{}')`); err != nil {
		return fmt.Errorf("append probe failed: %w", err)
	}
	if _, err := d.db.ExecContext(ctx, `UPDATE audit_events SET actor='tampered' WHERE event_type='probe'`); err == nil {
		return fmt.Errorf("append-only violation: UPDATE succeeded")
	}
	if _, err := d.db.ExecContext(ctx, `DELETE FROM audit_events WHERE event_type='probe'`); err == nil {
		return fmt.Errorf("append-only violation: DELETE succeeded")
	}
	return nil
}
