package opencode

import (
	"context"
	"errors"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestParseVersionString(t *testing.T) {
	cases := []struct{ in, want string }{
		{"0.1.48", "0.1.48"},
		{"v0.1.48", "0.1.48"},
		{"opencode 0.1.48", "0.1.48"},
		{"version 1.2.3-dev", "1.2.3"},
		{"opencode version 0.2.10+1", "0.2.10"},
		{"no version here", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := parseVersionString(c.in); got != c.want {
			t.Errorf("parseVersionString(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSemverCmpAndAtLeast(t *testing.T) {
	if parseSemver("1.2.3").cmp(parseSemver("1.2.3")) != 0 {
		t.Error("equal not 0")
	}
	if parseSemver("1.2.3").cmp(parseSemver("1.2.4")) >= 0 {
		t.Error("1.2.3 < 1.2.4")
	}
	if parseSemver("2.0.0").cmp(parseSemver("1.99.99")) <= 0 {
		t.Error("2.0.0 > 1.99.99")
	}
	if !parseSemver("0.1.48").atLeast("0.1.0") {
		t.Error("0.1.48 >= 0.1.0")
	}
	if parseSemver("0.0.9").atLeast("0.1.0") {
		t.Error("0.0.9 !>= 0.1.0")
	}
	// Unparseable floor degrades gracefully (treated as satisfied).
	if !parseSemver("0.1.48").atLeast("garbage") {
		t.Error("unparseable floor should be treated as satisfied")
	}
	// Pre-release suffix stripped.
	if parseSemver("1.2.3-rc1").cmp(parseSemver("1.2.3")) != 0 {
		t.Error("pre-release should compare equal on the triple")
	}
}

func TestVersionResultProtocolIsOne(t *testing.T) {
	a := stubAdapter("", "", 0, false, "")
	vr := a.Version(context.Background())
	if vr.ProtocolVersion != protocol.ProtocolVersion {
		t.Fatalf("protocol = %d, want %d", vr.ProtocolVersion, protocol.ProtocolVersion)
	}
	if vr.AdapterVersion == "" {
		t.Error("adapter version empty")
	}
	if vr.EngineVersion != "0.1.48" {
		t.Errorf("engine version = %q", vr.EngineVersion)
	}
}

func TestVersionResultErrorWhenAbsent(t *testing.T) {
	a := New(Options{})
	a.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	vr := a.Version(context.Background())
	if vr.Error == "" {
		t.Errorf("expected non-fatal error when absent, got %+v", vr)
	}
}

func TestCapabilitiesForVersion(t *testing.T) {
	// Modern version: full profile.
	caps := capabilitiesForVersion("0.1.48")
	check := func(name string, got, want bool) {
		if got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	check("HeadlessMode", caps.HeadlessMode, true)
	check("StreamingEvents", caps.StreamingEvents, true)
	check("StructuredOutput", caps.StructuredOutput, true)
	check("ModelSelection", caps.ModelSelection, true)
	check("SessionResume", caps.SessionResume, true)
	check("CachedUsageReporting", caps.CachedUsageReporting, true)
	check("MCP", caps.MCP, true)
	check("ACP", caps.ACP, true)
	check("LiveUserMessages", caps.LiveUserMessages, false)
	check("NativeSandbox", caps.NativeSandbox, false)
	check("InteractiveMode", caps.InteractiveMode, false)

	// Unknown/empty version: conservative baseline; version-gated features off.
	baseline := capabilitiesForVersion("")
	if baseline.HeadlessMode != true {
		t.Error("baseline must keep headless on")
	}
	if baseline.SessionResume {
		t.Error("baseline must not claim SessionResume for unknown version (§36.25)")
	}
	if baseline.CachedUsageReporting {
		t.Error("baseline must not claim CachedUsageReporting for unknown version")
	}
}
