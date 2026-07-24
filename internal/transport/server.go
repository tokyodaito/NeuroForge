package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Config configures a loopback API server.
type Config struct {
	// Addr is the listen address. It MUST resolve to loopback; non-loopback
	// binds are refused. Defaults to "127.0.0.1:0" (random loopback port).
	Addr string
	// Token is the required bearer credential. Must be >= MinTokenLen.
	Token string
	// OnShutdownRequest is invoked when a client POSTs /shutdown with a valid
	// token. The daemon wires it to cancel its run context (graceful stop).
	OnShutdownRequest func()
	// AuditReader, if set, backs the read-only GET /audit endpoint. Nil makes
	// /audit return 503 (e.g. in tests that do not wire storage).
	AuditReader AuditReader
	// ProjectAPI, if set, backs the project management endpoints (/projects).
	// Nil makes those endpoints return 503.
	ProjectAPI ProjectAPI
	// TaskAPI, if set, backs the task management endpoints (/tasks).
	// Nil makes those endpoints return 503.
	TaskAPI TaskAPI
}

// HealthResponse is the JSON body of GET /healthz.
type HealthResponse struct {
	Status    string `json:"status"`
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
	UptimeSec int64  `json:"uptime_sec"`
	Version   string `json:"version,omitempty"`
}

// Server is the loopback HTTP+SSE API server.
type Server struct {
	cfg       Config
	bus       *Bus
	logger    *slog.Logger
	ln        net.Listener
	srv       *http.Server
	startedAt time.Time
	pid       int
	version   string
}

var ErrNonLoopbackBind = errors.New("transport: refusing non-loopback bind")
var ErrMissingToken = errors.New("transport: config Token is required")
var ErrShortToken = errors.New("transport: config Token too short")

// NewServer validates the configuration (token presence/length, loopback
// address) and returns a server that is not yet listening.
func NewServer(cfg Config, bus *Bus, logger *slog.Logger) (*Server, error) {
	if cfg.Token == "" {
		return nil, ErrMissingToken
	}
	if len(cfg.Token) < MinTokenLen {
		return nil, fmt.Errorf("%w: need >= %d chars, got %d", ErrShortToken, MinTokenLen, len(cfg.Token))
	}
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:0"
	}
	if err := requireLoopbackHost(cfg.Addr); err != nil {
		return nil, err
	}
	if bus == nil {
		return nil, errors.New("transport: bus is required")
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Server{cfg: cfg, bus: bus, logger: logger}, nil
}

// SetVersion records the forge version to include in health responses.
func (s *Server) SetVersion(v string) { s.version = v }

// Listen binds the loopback listener and verifies (again, post-bind) that the
// resolved address is loopback. It returns the actual address.
func (s *Server) Listen() (net.Addr, error) {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("transport: listen %q: %w", s.cfg.Addr, err)
	}
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok || !tcp.IP.IsLoopback() {
		_ = ln.Close()
		return nil, fmt.Errorf("%w: %s", ErrNonLoopbackBind, ln.Addr().String())
	}
	s.ln = ln
	s.startedAt = time.Now().UTC()
	s.pid = os.Getpid()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.withToken(s.handleHealth))
	mux.HandleFunc("/status", s.withToken(s.handleStatus))
	mux.HandleFunc("/events", s.withToken(s.handleEvents))
	mux.HandleFunc("/shutdown", s.withToken(s.handleShutdown))
	mux.HandleFunc("/audit", s.withToken(s.handleAudit))
	s.registerAPIRoutes(mux)
	mux.HandleFunc("/", s.handleRoot)

	s.srv = &http.Server{
		Handler:           loggingMiddleware(s.logger, mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return ln.Addr(), nil
}

// Addr returns the bound address after Listen, or "" if not listening.
func (s *Server) Addr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Serve blocks until ctx is done, then gracefully shuts down the HTTP server.
// Listen must have been called first.
func (s *Server) Serve(ctx context.Context) error {
	if s.srv == nil || s.ln == nil {
		return errors.New("transport: Listen must be called before Serve")
	}
	errCh := make(chan error, 1)
	go func() { errCh <- s.srv.Serve(s.ln) }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// ---- handlers ----

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, HealthResponse{
		Status:    "ok",
		PID:       s.pid,
		StartedAt: s.startedAt.Format(time.RFC3339Nano),
		UptimeSec: int64(time.Since(s.startedAt).Seconds()),
		Version:   s.version,
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"pid":        s.pid,
		"started_at": s.startedAt.Format(time.RFC3339Nano),
		"uptime_sec": int64(time.Since(s.startedAt).Seconds()),
		"version":    s.version,
		"addr":       s.Addr(),
	})
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "shutting down"})
	if s.cfg.OnShutdownRequest != nil {
		go s.cfg.OnShutdownRequest()
	}
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Send an initial hello so the client knows the stream is live.
	hello, _ := json.Marshal(Event{Type: "stream.open", Ts: time.Now().UTC().Format(time.RFC3339Nano)})
	fmt.Fprintf(w, "data: %s\n\n", hello)
	flusher.Flush()

	ch, cancel := s.bus.Subscribe(defaultSubscribeBuf)
	defer cancel()

	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		// Unauthenticated 404 for unknown paths (do not reveal token logic).
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	// Root requires token too.
	if !checkToken(r, s.cfg.Token) {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":      "neuroforge-daemon",
		"endpoints": []string{"/healthz", "/status", "/events", "/audit", "/shutdown"},
	})
}

// ---- auth & helpers ----

// withToken wraps a handler requiring a valid bearer token. Accepts the token
// via the Authorization: Bearer header or a ?token= query parameter (the latter
// accommodates SSE clients that cannot set headers).
func (s *Server) withToken(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !checkToken(r, s.cfg.Token) {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		h(w, r)
	}
}

func checkToken(r *http.Request, token string) bool {
	if auth := r.Header.Get("Authorization"); auth != "" {
		const prefix = "Bearer "
		if strings.HasPrefix(auth, prefix) && subtleEqual(auth[len(prefix):], token) {
			return true
		}
	}
	if q := r.URL.Query().Get("token"); q != "" && subtleEqual(q, token) {
		return true
	}
	return false
}

// subtleEqual does a constant-time comparison to avoid timing oracles on the
// token. (Loopback-only, but cheap hygiene.)
func subtleEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

// requireLoopbackHost validates that host in a host:port pair is loopback or
// "localhost". An empty host (":port" -> all interfaces) is rejected.
func requireLoopbackHost(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// Allow addresses without a port (rare); treat the whole thing as host.
		host = addr
	}
	if host == "" {
		return fmt.Errorf("%w: empty host implies all interfaces", ErrNonLoopbackBind)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	// Resolve and verify every returned address is loopback.
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("transport: resolve %q: %w", host, err)
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return fmt.Errorf("%w: %s resolves to non-loopback %s", ErrNonLoopbackBind, host, ip)
		}
	}
	return nil
}

// loggingMiddleware logs HTTP requests with a scrubbed token (never log the
// Authorization value).
func loggingMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		logger.Debug("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"dur_ms", time.Since(start).Milliseconds(),
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Flush proxies to the underlying ResponseWriter so SSE still works through the
// middleware.
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
