package codex

import (
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestParseCodexVersion(t *testing.T) {
	cases := []struct {
		in           string
		valid        bool
		major, minor int
	}{
		{"codex 0.1.2505221", true, 0, 1},
		{"codex 0.42.0", true, 0, 42},
		{"codex 1.2.3", true, 1, 2},
		{"0.13.0", true, 0, 13},
		{"codex 2.0.0 (release) ", true, 2, 0},
		{"codex v0.5.1-rc1", true, 0, 5},
		{"codex", false, 0, 0},
		{"not a version at all", false, 0, 0},
		{"", false, 0, 0},
	}
	for _, c := range cases {
		pv := parseCodexVersion(c.in)
		if pv.valid != c.valid {
			t.Errorf("parseCodexVersion(%q).valid = %v, want %v", c.in, pv.valid, c.valid)
			continue
		}
		if c.valid && (pv.major != c.major || pv.minor != c.minor) {
			t.Errorf("parseCodexVersion(%q) = %d.%d, want %d.%d", c.in, pv.major, pv.minor, c.major, c.minor)
		}
	}
}

func TestParseCodexVersionPreservesRaw(t *testing.T) {
	pv := parseCodexVersion("codex 0.42.0 (release)")
	if pv.raw != "codex 0.42.0 (release)" {
		t.Errorf("raw = %q", pv.raw)
	}
}

func TestParsedVersionAtLeast(t *testing.T) {
	pv := parseCodexVersion("codex 0.42.0")
	if !pv.atLeast(0, 1) {
		t.Error("0.42 should be >= 0.1")
	}
	if pv.atLeast(1, 0) {
		t.Error("0.42 should not be >= 1.0")
	}
	if (parsedVersion{}).atLeast(0, 1) {
		t.Error("invalid version should not satisfy atLeast")
	}
}

func TestDeriveCapabilitiesDetected(t *testing.T) {
	pv := parseCodexVersion("codex 0.42.0")
	caps := deriveCapabilities(pv)
	checks := map[string]bool{
		"HeadlessMode":         caps.HeadlessMode,
		"StreamingEvents":      caps.StreamingEvents,
		"StructuredOutput":     caps.StructuredOutput,
		"ModelSelection":       caps.ModelSelection,
		"NativeSandbox":        caps.NativeSandbox,
		"ToolPermissions":      caps.ToolPermissions,
		"UsageReporting":       caps.UsageReporting,
		"CachedUsageReporting": caps.CachedUsageReporting,
		"SessionResume":        caps.SessionResume,
	}
	for name, v := range checks {
		if !v {
			t.Errorf("detected version: %s should be true", name)
		}
	}
	if caps.LiveUserMessages {
		t.Error("LiveUserMessages must be false for headless exec")
	}
	if caps.ImageInput || caps.MCP || caps.ACP {
		t.Error("ImageInput/MCP/ACP must not be claimed by default")
	}
}

func TestDeriveCapabilitiesUnknownVersionIsConservative(t *testing.T) {
	caps := deriveCapabilities(parsedVersion{major: -1, minor: -1, patch: -1})
	// An undetectable version reports no feature the adapter cannot confirm
	// (rule §36.25): never disguise an unconfirmed feature as supported.
	if caps.HeadlessMode || caps.SessionResume || caps.ModelSelection {
		t.Errorf("unknown version over-claimed capabilities: %+v", caps)
	}
}

func TestVersionMethodProtocolIsOne(t *testing.T) {
	fr := &fakeRunner{version: "codex 0.42.0"}
	a := newTestAdapter(fr)
	v := a.Version(t.Context())
	if v.ProtocolVersion != protocol.ProtocolVersion {
		t.Fatalf("ProtocolVersion = %d, want %d", v.ProtocolVersion, protocol.ProtocolVersion)
	}
	if v.EngineVersion != "codex 0.42.0" {
		t.Errorf("EngineVersion = %q", v.EngineVersion)
	}
	if v.AdapterVersion == "" {
		t.Error("AdapterVersion empty")
	}
}

func TestVersionMethodUnparsableFlagged(t *testing.T) {
	fr := &fakeRunner{version: "codex-some-weird-build"}
	a := newTestAdapter(fr)
	v := a.Version(t.Context())
	if v.ProtocolVersion != protocol.ProtocolVersion {
		t.Fatalf("ProtocolVersion = %d", v.ProtocolVersion)
	}
	if v.Error == "" {
		t.Error("expected a non-empty Error for an unparsable version")
	}
}
