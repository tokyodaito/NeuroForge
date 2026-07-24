package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNewServer_RejectsMissingToken(t *testing.T) {
	if _, err := NewServer(Config{Addr: "127.0.0.1:0"}, NewBus(), nil); err != ErrMissingToken {
		t.Fatalf("err = %v, want ErrMissingToken", err)
	}
}

func TestNewServer_RejectsShortToken(t *testing.T) {
	_, err := NewServer(Config{Addr: "127.0.0.1:0", Token: "tooshort"}, NewBus(), nil)
	if err == nil {
		t.Fatal("expected error for short token")
	}
}

func TestNewServer_RejectsNonLoopbackBind(t *testing.T) {
	token, _ := GenerateToken()
	for _, addr := range []string{"0.0.0.0:8080", ":8080", "8.8.8.8:8080"} {
		t.Run(addr, func(t *testing.T) {
			_, err := NewServer(Config{Addr: addr, Token: token}, NewBus(), nil)
			if err == nil {
				t.Fatalf("expected non-loopback refusal for %q", addr)
			}
			if !strings.Contains(err.Error(), "loopback") && err != ErrNonLoopbackBind {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestNewServer_AcceptsLoopbackBind(t *testing.T) {
	token, _ := GenerateToken()
	for _, addr := range []string{"127.0.0.1:0", "localhost:0", "[::1]:0"} {
		t.Run(addr, func(t *testing.T) {
			srv, err := NewServer(Config{Addr: addr, Token: token}, NewBus(), nil)
			if err != nil {
				t.Fatalf("expected accept for %q: %v", addr, err)
			}
			ln, err := srv.Listen()
			if err != nil {
				t.Fatalf("Listen %q: %v", addr, err)
			}
			defer srv.srv.Close()
			if !strings.HasPrefix(ln.String(), "127.") && !strings.Contains(ln.String(), "::1") {
				t.Fatalf("addr not loopback: %s", ln.String())
			}
		})
	}
}

func startTestServer(t *testing.T, onShutdown func()) (*Server, string, string) {
	t.Helper()
	token, err := GenerateToken()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	bus := NewBus()
	srv, err := NewServer(Config{Addr: "127.0.0.1:0", Token: token, OnShutdownRequest: onShutdown}, bus, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	addr, err := srv.Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = srv.srv.Close() })
	go func() { _ = srv.Serve(context.Background()) }()
	// Give the server a moment to start serving.
	time.Sleep(20 * time.Millisecond)
	return srv, "http://" + addr.String(), token
}

func TestAuth_RejectsMissingOrWrongToken(t *testing.T) {
	_, base, token := startTestServer(t, nil)
	ctx := context.Background()

	// No token -> 401
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/healthz", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", resp.StatusCode)
	}

	// Wrong token -> 401
	req, _ = http.NewRequestWithContext(ctx, "GET", base+"/healthz", nil)
	req.Header.Set("Authorization", "Bearer wrongtoken")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: status = %d, want 401", resp.StatusCode)
	}

	// Correct token -> 200
	cli := NewClient(base, token)
	if _, err := cli.Health(ctx); err != nil {
		t.Fatalf("correct token health: %v", err)
	}
}

func TestHealthEndpoint(t *testing.T) {
	srv, base, token := startTestServer(t, nil)
	srv.SetVersion("test-1")
	cli := NewClient(base, token)
	hr, err := cli.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if hr.Status != "ok" {
		t.Fatalf("status = %q", hr.Status)
	}
	if hr.PID == 0 {
		t.Fatal("pid empty")
	}
	if hr.Version != "test-1" {
		t.Fatalf("version = %q", hr.Version)
	}
}

func TestShutdownEndpoint_TriggersCallback(t *testing.T) {
	done := make(chan struct{}, 1)
	_, base, token := startTestServer(t, func() { done <- struct{}{} })
	cli := NewClient(base, token)
	if err := cli.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback not invoked")
	}
}

func TestSSE_DeliversEventsInOrder(t *testing.T) {
	srv, base, token := startTestServer(t, nil)
	bus := srv.bus

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cli := NewClient(base, token)
	ch, err := cli.Stream(ctx)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	// Drain the initial stream.open hello.
	<-ch

	// Publish a sequence of events and expect them in order.
	const n = 5
	for i := 0; i < n; i++ {
		bus.Publish("test.event", map[string]any{"i": i})
	}

	var got []int64
	for i := 0; i < n; i++ {
		select {
		case evt := <-ch:
			got = append(got, evt.Seq)
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for event %d; got %d", i, len(got))
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("events not in order: %v", got)
		}
	}
}

func TestBus_PublishNonBlocking(t *testing.T) {
	bus := NewBus()
	defer bus.Close()
	ch, cancel := bus.Subscribe(1) // tiny buffer
	defer cancel()

	// Publish more than the buffer; must not block.
	for i := 0; i < 100; i++ {
		bus.Publish("x", i)
	}
	// At least the hello/first events should be receivable.
	select {
	case <-ch:
	default:
		t.Fatal("expected at least one event in buffered channel")
	}
}

func TestBus_CloseDropsSubscribers(t *testing.T) {
	bus := NewBus()
	ch, _ := bus.Subscribe(2)
	bus.Close()
	// Channel must be closed by Close -> receive yields ok=false.
	if _, ok := <-ch; ok {
		t.Fatal("expected subscriber channel to be closed after Bus.Close")
	}
	// Publish after close is a no-op (no panic).
	bus.Publish("late", nil)
}

func TestEvent_JSONRoundTrip(t *testing.T) {
	e := Event{Seq: 7, Type: "t", Ts: "2026-01-02T03:04:05Z", Data: map[string]any{"k": "v"}}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Event
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Seq != 7 || got.Type != "t" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}
