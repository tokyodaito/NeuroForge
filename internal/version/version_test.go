package version

import (
	"runtime"
	"strings"
	"testing"
)

func TestCurrent_UsesBuildVars(t *testing.T) {
	info := Current()
	if info.Version != Version {
		t.Fatalf("Version = %q, want %q", info.Version, Version)
	}
	if info.Commit != Commit {
		t.Fatalf("Commit = %q, want %q", info.Commit, Commit)
	}
	if info.Date != Date {
		t.Fatalf("Date = %q, want %q", info.Date, Date)
	}
}

func TestCurrent_PopulatesRuntimeFields(t *testing.T) {
	info := Current()
	if info.GoVersion == "" {
		t.Fatal("GoVersion is empty")
	}
	if info.OS == "" {
		t.Fatal("OS is empty")
	}
	if info.Arch == "" {
		t.Fatal("Arch is empty")
	}
	if !strings.HasPrefix(info.GoVersion, "go") {
		t.Fatalf("GoVersion %q should start with 'go'", info.GoVersion)
	}
}

func TestCurrent_RuntimeConsistentWithRuntimePackage(t *testing.T) {
	info := Current()
	if info.GoVersion != runtime.Version() {
		t.Fatalf("GoVersion = %q, want %q", info.GoVersion, runtime.Version())
	}
	if info.OS != runtime.GOOS {
		t.Fatalf("OS = %q, want %q", info.OS, runtime.GOOS)
	}
	if info.Arch != runtime.GOARCH {
		t.Fatalf("Arch = %q, want %q", info.Arch, runtime.GOARCH)
	}
}

func TestInfo_String_ContainsAllFields(t *testing.T) {
	info := Info{
		Version:   "1.2.3",
		Commit:    "abc1234",
		Date:      "2026-01-02T03:04:05Z",
		GoVersion: "go1.26.0",
		OS:        "darwin",
		Arch:      "arm64",
	}
	got := info.String()
	for _, want := range []string{
		"forge 1.2.3",
		"abc1234",
		"2026-01-02T03:04:05Z",
		"go1.26.0",
		"darwin/arm64",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("String() missing %q; got:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("String() should end with newline; got %q", got)
	}
}

func TestInfo_String_EndsWithPlatform(t *testing.T) {
	info := Info{Version: "9.9.9", OS: "linux", Arch: "amd64"}
	got := info.String()
	if !strings.Contains(got, "platform: linux/amd64") {
		t.Errorf("expected platform line; got:\n%s", got)
	}
}
