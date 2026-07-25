package storage

import (
	"context"
	"fmt"
	"time"
)

// PostMergeCheckRow is one persisted post_merge_checks row (spec §31, §37,
// milestone M12). It is the durable record of a post-merge sentinel run.
type PostMergeCheckRow struct {
	ID         int64
	TaskID     string
	CommitSHA  string
	BaseBranch string
	Decision   string // HEALTHY | REVERT | ALERT_ONLY | SKIPPED
	AllPassed  bool
	Reverted   bool
	RevertSHA  string
	OccurredAt time.Time
	// ChecksJSON is the serialised list of check results (one row per sentinel
	// run, the per-check detail kept as JSON so the schema stays stable).
	ChecksJSON string
}

// RecordPostMergeCheck inserts one post-merge check result row and returns its
// assigned id.
func (d *DB) RecordPostMergeCheck(ctx context.Context, r PostMergeCheckRow) (int64, error) {
	if r.OccurredAt.IsZero() {
		r.OccurredAt = time.Now().UTC()
	}
	allPassed := 0
	if r.AllPassed {
		allPassed = 1
	}
	reverted := 0
	if r.Reverted {
		reverted = 1
	}
	if r.ChecksJSON == "" {
		r.ChecksJSON = "[]"
	}
	res, err := d.db.ExecContext(ctx, `
INSERT INTO post_merge_checks
	(task_id, commit_sha, base_branch, decision, all_passed, reverted, revert_sha, occurred_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.TaskID, r.CommitSHA, r.BaseBranch, r.Decision, allPassed, reverted, r.RevertSHA,
		r.OccurredAt.Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("storage: record post-merge check: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("storage: post-merge check last insert id: %w", err)
	}
	return id, nil
}

// ListPostMergeChecks returns the post-merge check rows for a task, oldest
// first.
func (d *DB) ListPostMergeChecks(ctx context.Context, taskID string) ([]PostMergeCheckRow, error) {
	q := `SELECT id, task_id, commit_sha, base_branch, decision, all_passed, reverted,
		revert_sha, occurred_at
	FROM post_merge_checks WHERE task_id = ? ORDER BY id ASC`
	rows, err := d.db.QueryContext(ctx, q, taskID)
	if err != nil {
		return nil, fmt.Errorf("storage: query post_merge_checks: %w", err)
	}
	defer rows.Close()
	var out []PostMergeCheckRow
	for rows.Next() {
		var r PostMergeCheckRow
		var allPassed, reverted int
		var occurredAt string
		if err := rows.Scan(&r.ID, &r.TaskID, &r.CommitSHA, &r.BaseBranch, &r.Decision,
			&allPassed, &reverted, &r.RevertSHA, &occurredAt); err != nil {
			return nil, fmt.Errorf("storage: scan post-merge check: %w", err)
		}
		r.AllPassed = allPassed == 1
		r.Reverted = reverted == 1
		r.OccurredAt, _ = time.Parse(time.RFC3339Nano, occurredAt)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterate post-merge checks: %w", err)
	}
	return out, nil
}
