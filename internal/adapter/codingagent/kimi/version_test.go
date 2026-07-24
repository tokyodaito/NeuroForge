package kimi

import (
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want parsedVersion
	}{
		{"Kimi Code 1.4.0", parsedVersion{1, 4, 0, true}},
		{"kimi v2.0.1", parsedVersion{2, 0, 1, true}},
		{"1.3", parsedVersion{1, 3, 0, true}},
		{"v0.9.0-beta", parsedVersion{0, 9, 0, true}},
		{"no version here", parsedVersion{ok: false}},
		{"", parsedVersion{ok: false}},
	}
	for _, c := range cases {
		got := parseVersion(c.in)
		if got != c.want {
			t.Errorf("parseVersion(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestVersionProfileGating(t *testing.T) {
	// Unknown/old version: baseline capabilities only, no streaming/resume.
	p := newVersionProfile(parsedVersion{}, false)
	if !p.caps.HeadlessMode || !p.caps.ModelSelection || !p.caps.StructuredOutput {
		t.Errorf("baseline caps missing: %+v", p.caps)
	}
	if p.caps.StreamingEvents || p.caps.SessionResume || p.caps.UsageReporting {
		t.Errorf("old version should not report streaming/resume/usage: %+v", p.caps)
	}
	if p.flagStreamJSON || p.flagContinue || p.flagMaxTurns {
		t.Errorf("old version flags wrong: %+v", p)
	}

	// 1.2.x: streaming + cached usage + max-turns, no resume.
	p = newVersionProfile(parsedVersion{1, 2, 0, true}, false)
	if !p.caps.StreamingEvents || !p.caps.UsageReporting || !p.caps.CachedUsageReporting {
		t.Errorf("1.2.0 caps wrong: %+v", p.caps)
	}
	if !p.flagStreamJSON || !p.flagMaxTurns {
		t.Errorf("1.2.0 flags wrong: %+v", p)
	}
	if p.caps.SessionResume || p.flagContinue {
		t.Errorf("1.2.0 should not support resume: %+v", p)
	}

	// 1.3.0: resume.
	p = newVersionProfile(parsedVersion{1, 3, 0, true}, false)
	if !p.caps.SessionResume || !p.flagContinue {
		t.Errorf("1.3.0 should support resume: %+v", p)
	}

	// ForceStreaming upgrades an old version.
	p = newVersionProfile(parsedVersion{0, 1, 0, true}, true)
	if !p.caps.StreamingEvents || !p.flagStreamJSON {
		t.Errorf("ForceStreaming should enable streaming on old version: %+v", p)
	}
}

func TestCapabilitiesOverrideMerges(t *testing.T) {
	// An override can only ADD capability (union), never remove a real one.
	a := New(Options{
		BinaryOverride: "/nonexistent/kimi",
		Capabilities:   &protocol.AgentCapabilities{MCP: true, ImageInput: true},
	})
	// Force a probe with an unresolvable override; ensureProbe still runs and
	// records installed=false. Capabilities merges override + baseline.
	caps := a.Capabilities(testContext())
	if !caps.MCP || !caps.ImageInput {
		t.Errorf("override caps dropped: %+v", caps)
	}
	if !caps.HeadlessMode {
		t.Errorf("merged caps lost baseline HeadlessMode: %+v", caps)
	}
}
