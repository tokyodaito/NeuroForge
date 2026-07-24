//go:build opencodesmoke

// This file is compiled ONLY when the `opencodesmoke` build tag is passed, so it
// is excluded from all normal/CI test runs. It additionally requires the
// OPENCODE_SMOKE=1 environment variable and is skipped in -short mode, making a
// real-binary invocation triple-opt-in.
//
// To honour rule §36.5 (no real paid models in tests), this smoke test exercises
// ONLY the detection/version/health/capabilities surface against an installed
// OpenCode binary. It NEVER starts an `opencode run` (which would route to a
// real, paid provider model); end-to-end run behaviour is covered offline by the
// conformance suite against recorded byte-stream fixtures.

package opencode

import (
	"context"
	"os"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestSmokeRealBinaryMetadata(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke test skipped in -short mode")
	}
	if os.Getenv("OPENCODE_SMOKE") != "1" {
		t.Skip("set OPENCODE_SMOKE=1 to run the real-binary smoke test")
	}

	a := New(Options{}) // real exec.LookPath + real `opencode --version` probe
	ctx, cancel := context.WithTimeout(context.Background(), smokeTimeout)
	defer cancel()

	d := a.Detect(ctx)
	if !d.Installed {
		t.Fatalf("opencode not detected on this host: %+v\n"+
			"install opencode or unset OPENCODE_SMOKE to skip", d)
	}
	t.Logf("detected: path=%s version=%s detail=%s", d.Path, d.Version, d.Detail)

	vr := a.Version(ctx)
	if vr.ProtocolVersion != protocol.ProtocolVersion {
		t.Errorf("protocol = %d, want %d", vr.ProtocolVersion, protocol.ProtocolVersion)
	}
	if vr.EngineVersion == "" {
		t.Logf("engine version not parsed from --version output (non-fatal)")
	}

	caps := a.Capabilities(ctx)
	if !caps.HeadlessMode {
		t.Error("expected headless mode support from a real opencode install")
	}

	h := a.Health(ctx, protocol.Account{})
	switch h.Status {
	case protocol.HealthOK, protocol.HealthDegraded:
		// acceptable for a present binary
	default:
		t.Errorf("health = %s for an installed binary", h.Status)
	}
}

// smokeTimeout is generous: `opencode --version` is local and fast, but Windows
// process spawn + module init can take a moment on cold caches.
const smokeTimeout = 30 * time.Second
