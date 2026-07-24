package daemon

import (
	"context"

	"neuroforge/internal/storage"
	"neuroforge/internal/transport"
)

// auditReader adapts *storage.DB to the transport.AuditReader interface so the
// read-only /audit endpoint can serve durable audit history without the
// transport package depending on storage.
type auditReader struct {
	db *storage.DB
}

// AuditEntries returns the newest-first audit history (capped at limit).
func (a *auditReader) AuditEntries(ctx context.Context, limit int) ([]transport.AuditEntry, error) {
	rows, err := a.db.ListAuditEvents(ctx, storage.AuditFilter{Limit: limit, NewestFirst: true})
	if err != nil {
		return nil, err
	}
	out := make([]transport.AuditEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, transport.AuditEntry{
			ID:      r.ID,
			Ts:      r.Timestamp,
			Scope:   r.Scope,
			ScopeID: r.ScopeID,
			Type:    r.Type,
			Actor:   r.Actor,
			Payload: r.Payload,
		})
	}
	return out, nil
}
