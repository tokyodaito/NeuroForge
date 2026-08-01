package transport

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"
)

// fakePipelineAPI is a minimal test double for transport.PipelineAPI covering
// the estop endpoints.
type fakePipelineAPI struct {
	onSet  bool
	on     bool
	reason string
}

func (f *fakePipelineAPI) RunPipeline(ctx context.Context, req PipelineRunRequest) (PipelineRunResultDTO, error) {
	return PipelineRunResultDTO{}, nil
}
func (f *fakePipelineAPI) PipelineStatus(ctx context.Context, taskID string) (PipelineRunResultDTO, error) {
	return PipelineRunResultDTO{}, nil
}
func (f *fakePipelineAPI) CancelPipeline(ctx context.Context, taskID string) (PipelineRunResultDTO, error) {
	return PipelineRunResultDTO{}, nil
}
func (f *fakePipelineAPI) SetEmergencyStop(ctx context.Context, on bool, reason string) (EstopDTO, error) {
	f.onSet, f.on, f.reason = true, on, reason
	return EstopDTO{On: on, Reason: reason}, nil
}
func (f *fakePipelineAPI) EmergencyStopStatus(ctx context.Context) (EstopDTO, error) {
	return EstopDTO{On: f.on, Reason: f.reason}, nil
}

func startEstopTestServer(t *testing.T, api PipelineAPI) (string, string) {
	t.Helper()
	srv, err := NewServer(Config{
		Addr:        "127.0.0.1:0",
		Token:       "test-token-that-is-long-enough-32+chars",
		PipelineAPI: api,
	}, NewBus(), nil)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := srv.Listen()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		time.Sleep(50 * time.Millisecond)
	})
	return "http://" + addr.String(), "test-token-that-is-long-enough-32+chars"
}

// TestEstopSet_RequiresOnField (L1): a POST /estop without an explicit "on"
// field (empty body, or a body lacking the field) must be REJECTED — it must
// never decode to On=false and silently clear the stop.
func TestEstopSet_RequiresOnField(t *testing.T) {
	api := &fakePipelineAPI{}
	base, token := startEstopTestServer(t, api)

	post := func(body string) *http.Response {
		t.Helper()
		var rdr *bytes.Reader
		if body == "" {
			rdr = bytes.NewReader(nil)
		} else {
			rdr = bytes.NewReader([]byte(body))
		}
		req, err := http.NewRequest(http.MethodPost, base+"/estop", rdr)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST /estop: %v", err)
		}
		defer resp.Body.Close()
		return resp
	}

	if resp := post(""); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("empty body: status = %d, want 400", resp.StatusCode)
	}
	if resp := post(`{"reason":"x"}`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("body without on: status = %d, want 400", resp.StatusCode)
	}
	if api.onSet {
		t.Error("SetEmergencyStop must not be called when \"on\" is absent")
	}

	if resp := post(`{"on":false}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("explicit on=false: status = %d, want 200", resp.StatusCode)
	}
	if !api.onSet || api.on {
		t.Error("explicit on=false must reach SetEmergencyStop(false)")
	}
	if resp := post(`{"on":true,"reason":"halt"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("explicit on=true: status = %d, want 200", resp.StatusCode)
	}
	if !api.on || api.reason != "halt" {
		t.Errorf("explicit on=true with reason: got on=%v reason=%q", api.on, api.reason)
	}
}
