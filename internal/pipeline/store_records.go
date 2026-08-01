// This file adds a read-only accessor over the pipeline stage history. It
// changes no Store semantics: the daemon's pipeline service needs the durable
// per-stage records to render run status (transport GET /tasks/{id}/pipeline)
// and the CLI stage-progression summary. The records are append-only; this is
// a plain ordered SELECT.
package pipeline

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// StageRecords returns the full stage history for a task's run, ordered by
// record id (insertion order). A task with no run yields an empty slice (not
// ErrRunNotFound — the history of a missing run is empty).
func (s *Store) StageRecords(ctx context.Context, taskID string) ([]StageRecord, error) {
	rows, err := s.db.Underlying().QueryContext(ctx, `
SELECT id, task_id, stage, attempt, status, failure_category, reason, evidence_ref, entered_at, finished_at
FROM pipeline_stage_records
WHERE task_id = ?
ORDER BY id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("pipeline: list stage records for task %s: %w", taskID, err)
	}
	defer rows.Close()
	out := []StageRecord{}
	for rows.Next() {
		var rec StageRecord
		var st, statusStr, enteredAt string
		var finishedAt sql.NullString
		var category, evidence sql.NullString
		if err := rows.Scan(&rec.ID, &rec.TaskID, &st, &rec.Attempt, &statusStr,
			&category, &rec.Reason, &evidence, &enteredAt, &finishedAt); err != nil {
			return nil, fmt.Errorf("pipeline: scan stage record: %w", err)
		}
		rec.Stage = Stage(st)
		rec.Status = RecordStatus(statusStr)
		if category.Valid {
			rec.FailureCategory = FailureCategory(category.String)
		}
		if evidence.Valid {
			rec.EvidenceRef = evidence.String
		}
		var err error
		if rec.EnteredAt, err = time.Parse(time.RFC3339Nano, enteredAt); err != nil {
			return nil, fmt.Errorf("pipeline: parse entered_at: %w", err)
		}
		if finishedAt.Valid && finishedAt.String != "" {
			if rec.FinishedAt, err = time.Parse(time.RFC3339Nano, finishedAt.String); err != nil {
				return nil, fmt.Errorf("pipeline: parse finished_at: %w", err)
			}
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pipeline: list stage records for task %s: %w", taskID, err)
	}
	return out, nil
}
