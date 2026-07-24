package transport

import (
	"context"
	"net/http"
	"strconv"
)

// AuditEntry is the wire DTO for one audit row (a read-only subset of
// internal/storage.AuditEvent). It carries no secrets.
type AuditEntry struct {
	ID      int64  `json:"id"`
	Ts      string `json:"ts"`
	Scope   string `json:"scope"`
	ScopeID string `json:"scope_id"`
	Type    string `json:"type"`
	Actor   string `json:"actor"`
	Payload string `json:"payload"`
}

// AuditReader supplies audit entries to the read-only /audit endpoint. The
// daemon implements it over durable storage; the transport package depends only
// on this interface (no storage import).
type AuditReader interface {
	AuditEntries(ctx context.Context, limit int) ([]AuditEntry, error)
}

// handleAudit serves GET /audit?limit=N (newest-first). Requires the token.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.cfg.AuditReader == nil {
		writeErr(w, http.StatusServiceUnavailable, "audit reader not configured")
		return
	}
	limit := 100
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 10000 {
			limit = n
		}
	}
	entries, err := s.cfg.AuditReader.AuditEntries(r.Context(), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read audit: "+err.Error())
		return
	}
	if entries == nil {
		entries = []AuditEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}
