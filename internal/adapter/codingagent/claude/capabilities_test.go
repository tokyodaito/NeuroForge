package claude

import (
	"context"
	"testing"
)

func newCapsAdapter(t *testing.T, probeOut string) *Adapter {
	t.Helper()
	a, err := New(Options{
		BinaryPath: "claude",
		LookPath:   func(string) (string, error) { return "claude", nil },
		Probe: func(ctx context.Context, name string, args []string, env []string) ([]byte, []byte, int, error) {
			if len(args) > 0 && args[0] == "--version" {
				return []byte(probeOut), nil, 0, nil
			}
			return nil, nil, 0, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestCapabilitiesBaseSet(t *testing.T) {
	a := newCapsAdapter(t, "2.1.205 (Claude Code)\n")
	caps := a.Capabilities(context.Background())
	truthy := []bool{caps.HeadlessMode, caps.StreamingEvents, caps.StructuredOutput,
		caps.ModelSelection, caps.UsageReporting, caps.CachedUsageReporting,
		caps.ToolPermissions, caps.SessionResume, caps.MCP}
	for i, v := range truthy {
		if !v {
			t.Errorf("base capability %d should be true: %+v", i, caps)
		}
	}
}

func TestCapabilitiesDoNotOverstate(t *testing.T) {
	a := newCapsAdapter(t, "2.1.205 (Claude Code)\n")
	caps := a.Capabilities(context.Background())
	// Capabilities the adapter does not wire up must be false (do not claim what
	// you do not have): live user messages, native sandbox, ACP, image input.
	if caps.LiveUserMessages {
		t.Error("LiveUserMessages should be false (headless text stdin only)")
	}
	if caps.NativeSandbox {
		t.Error("NativeSandbox should be false (permission system != sandbox)")
	}
	if caps.ACP {
		t.Error("ACP should be false")
	}
	if caps.ImageInput {
		t.Error("ImageInput should be false (adapter does not surface images)")
	}
}

func TestCapabilitiesForUnknownVersionSafe(t *testing.T) {
	// When the version cannot be parsed, capabilitiesFor still returns the safe
	// base set (never panics, never overstates).
	caps := capabilitiesFor(parsedVersion{}, false)
	if !caps.HeadlessMode || !caps.StreamingEvents {
		t.Errorf("base caps missing for unknown version: %+v", caps)
	}
}
