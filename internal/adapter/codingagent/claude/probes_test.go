package claude

import (
	"context"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func newHealthAdapter(t *testing.T, probe func(ctx context.Context, name string, args []string, env []string) ([]byte, []byte, int, error)) *Adapter {
	t.Helper()
	a, err := New(Options{
		BinaryPath: "claude",
		LookPath:   func(string) (string, error) { return "claude", nil },
		Probe:      probe,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestHealthOK(t *testing.T) {
	a := newHealthAdapter(t, func(ctx context.Context, name string, args []string, env []string) ([]byte, []byte, int, error) {
		if len(args) >= 2 && args[0] == "auth" && args[1] == "status" {
			return []byte(`{"loggedIn":true,"account":"me@example.com","mode":"console"}`), nil, 0, nil
		}
		return []byte("2.1.205\n"), nil, 0, nil
	})
	r := a.Health(context.Background(), protocol.Account{})
	if r.Status != protocol.HealthOK {
		t.Errorf("status = %s, want ok (%s)", r.Status, r.Detail)
	}
}

func TestHealthDegradedWhenNotLoggedIn(t *testing.T) {
	a := newHealthAdapter(t, func(ctx context.Context, name string, args []string, env []string) ([]byte, []byte, int, error) {
		if len(args) >= 2 && args[0] == "auth" && args[1] == "status" {
			return []byte(`{"loggedIn":false}`), nil, 1, nil
		}
		return []byte("2.1.205\n"), nil, 0, nil
	})
	r := a.Health(context.Background(), protocol.Account{})
	if r.Status != protocol.HealthDegraded {
		t.Errorf("status = %s, want degraded (%s)", r.Status, r.Detail)
	}
}

func TestHealthUnknownWhenAuthSubcommandMissing(t *testing.T) {
	a := newHealthAdapter(t, func(ctx context.Context, name string, args []string, env []string) ([]byte, []byte, int, error) {
		if len(args) >= 2 && args[0] == "auth" && args[1] == "status" {
			return nil, []byte("error: unknown command \"auth\"\n"), 1, nil
		}
		return []byte("0.9.0\n"), nil, 0, nil
	})
	r := a.Health(context.Background(), protocol.Account{})
	if r.Status != protocol.HealthUnknown {
		t.Errorf("status = %s, want unknown (%s)", r.Status, r.Detail)
	}
}

func TestHealthDownWhenMissing(t *testing.T) {
	a, _ := New(Options{LookPath: func(string) (string, error) { return "", errNotInstalled }})
	r := a.Health(context.Background(), protocol.Account{})
	if r.Status != protocol.HealthDown {
		t.Errorf("status = %s, want down", r.Status)
	}
}

func TestInspectQuotaUnknown(t *testing.T) {
	a, _ := New(Options{BinaryPath: "claude"})
	q := a.InspectQuota(context.Background(), protocol.Account{})
	if q.Confidence != protocol.QuotaConfUnknown {
		t.Errorf("confidence = %s, want UNKNOWN", q.Confidence)
	}
	if q.State != protocol.QuotaStateUnknown {
		t.Errorf("state = %s, want UNKNOWN", q.State)
	}
}

func TestListModelsDefaultsAndOverride(t *testing.T) {
	a, _ := New(Options{BinaryPath: "claude"})
	def, _ := a.ListModels(context.Background(), protocol.Account{})
	if len(def) == 0 {
		t.Fatal("default models empty")
	}
	for _, m := range def {
		if m.Engine != EngineID {
			t.Errorf("model engine = %q, want %s", m.Engine, EngineID)
		}
	}
	custom := []protocol.ModelDescriptor{{ID: "custom/x", Engine: EngineID, Kind: protocol.ModelKindCoding}}
	a2, _ := New(Options{BinaryPath: "claude", Models: custom})
	got, _ := a2.ListModels(context.Background(), protocol.Account{})
	if len(got) != 1 || got[0].ID != "custom/x" {
		t.Errorf("override not honoured: %+v", got)
	}
}

func TestAuthSubcommandMissingDetector(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"Error: unknown command \"auth\"", true},
		{"unknown subcommand: status", true},
		{"'auth' is not a recognized command", true},
		{"logged in as me", false},
	}
	for _, c := range cases {
		if got := authSubcommandMissing([]byte(c.in)); got != c.want {
			t.Errorf("authSubcommandMissing(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
