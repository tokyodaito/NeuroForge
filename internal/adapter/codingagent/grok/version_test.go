package grok

import "testing"

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in                  string
		major, minor, patch int
		known               bool
		str                 string
	}{
		{"grok version 1.2.3", 1, 2, 3, true, "1.2.3"},
		{"v0.9.0-beta", 0, 9, 0, true, "0.9.0"},
		{"0.10", 0, 10, 0, true, "0.10.0"},
		{"grok 2.0 (build 42)", 2, 0, 0, true, "2.0.0"},
		{"no version here", 0, 0, 0, false, "no version here"},
		{"", 0, 0, 0, false, ""},
		{"1", 1, 0, 0, true, "1.0.0"},
		{"v3", 3, 0, 0, true, "3.0.0"},
	}
	for _, c := range cases {
		v := parseVersion(c.in)
		if v.known != c.known {
			t.Errorf("parseVersion(%q).known = %v, want %v", c.in, v.known, c.known)
			continue
		}
		if v.known {
			if v.major != c.major || v.minor != c.minor || v.patch != c.patch {
				t.Errorf("parseVersion(%q) = %d.%d.%d, want %d.%d.%d", c.in, v.major, v.minor, v.patch, c.major, c.minor, c.patch)
			}
		}
		if got := v.String(); got != c.str {
			t.Errorf("parseVersion(%q).String() = %q, want %q", c.in, got, c.str)
		}
	}
}

func TestVersionAtLeast(t *testing.T) {
	if !parseVersion("1.2.3").atLeast(versionInfo{known: true, major: 1, minor: 2, patch: 3}) {
		t.Error("1.2.3 should be >= 1.2.3")
	}
	if parseVersion("1.2.2").atLeast(versionInfo{known: true, major: 1, minor: 2, patch: 3}) {
		t.Error("1.2.2 should not be >= 1.2.3")
	}
	if !parseVersion("2.0.0").atLeast(versionInfo{known: true, major: 1, minor: 9, patch: 9}) {
		t.Error("2.0.0 should be >= 1.9.9")
	}
}

func TestVersionMethodReportsProtocolV1(t *testing.T) {
	a := New(Options{})
	v := a.Version(testContext(t))
	if v.ProtocolVersion != 1 {
		t.Fatalf("ProtocolVersion = %d, want 1", v.ProtocolVersion)
	}
	if v.AdapterVersion == "" {
		t.Error("AdapterVersion is empty")
	}
}

func TestCapabilitiesDefaultShape(t *testing.T) {
	a := New(Options{})
	caps := a.Capabilities(testContext(t))
	if !caps.HeadlessMode || !caps.StreamingEvents || !caps.StructuredOutput {
		t.Error("core headless capabilities missing")
	}
	if !caps.ModelSelection {
		t.Error("ModelSelection should be true")
	}
	if caps.LiveUserMessages {
		t.Error("LiveUserMessages should be false (not implemented)")
	}
	if caps.ImageInput {
		t.Error("ImageInput should be false (coding/image separation, rule §36.9)")
	}
}
