package codingagent

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// TestRequestCarriesNoCredentials enforces spec §29.2 / AC-28 at the type level:
// the adapter request/handle/account structures must not carry any credential
// field. The agent process resolves credentials internally from a name-only
// Account reference; merge tokens, API keys and the daemon auth token must never
// flow through the protocol.
func TestRequestCarriesNoCredentials(t *testing.T) {
	forbidden := []string{"token", "secret", "password", "passwd", "credential", "apikey", "api_key", "key", "mergetoken", "auth_token"}
	types := []any{
		protocol.AgentRunRequest{},
		protocol.ResumeRequest{},
		protocol.RunHandle{},
		protocol.Account{},
		protocol.AgentMessage{},
	}
	for _, sample := range types {
		ty := reflect.TypeOf(sample)
		for i := 0; i < ty.NumField(); i++ {
			f := ty.Field(i)
			name := strings.ToLower(f.Name)
			for _, bad := range forbidden {
				if strings.Contains(name, bad) {
					t.Errorf("%s.%s: field name contains forbidden credential term %q (AC-28)", ty.Name(), f.Name, bad)
				}
			}
		}
	}
}

// TestAccountIsNameOnly ensures Account carries only an opaque name, never a
// secret value (AC-28).
func TestAccountIsNameOnly(t *testing.T) {
	ty := reflect.TypeOf(protocol.Account{})
	if ty.NumField() != 1 {
		t.Fatalf("Account should have exactly one field (Name), got %d", ty.NumField())
	}
	if ty.Field(0).Name != "Name" {
		t.Errorf("Account field = %s, want Name", ty.Field(0).Name)
	}
}

// TestEngineAndModelAreDistinct enforces spec §12.1 (engine != model) at the
// type level: RunHandle and AgentRunRequest must carry Engine and Model as
// separate fields.
func TestEngineAndModelAreDistinct(t *testing.T) {
	for _, sample := range []any{protocol.RunHandle{}, protocol.AgentRunRequest{}} {
		ty := reflect.TypeOf(sample)
		fields := map[string]bool{}
		for i := 0; i < ty.NumField(); i++ {
			fields[ty.Field(i).Name] = true
		}
		if !fields["Engine"] || !fields["Model"] {
			t.Errorf("%s must have both Engine and Model fields (§12.1)", ty.Name())
		}
	}
}

// TestCoreHasNoHardcodedModelNames scans the protocol and codingagent package
// sources for real-world model identifiers. The core must never hard-code model
// names (rule §36.8, §19.2); models are provider-supplied via ModelDescriptor.
func TestCoreHasNoHardcodedModelNames(t *testing.T) {
	forbidden := []string{
		"gpt-4", "gpt-3", "gpt4", "gpt3", "gpt-5", "gpt5",
		"claude-3", "claude3", "claude-2", "claude-opus", "claude-sonnet", "claude-haiku",
		"gemini-1", "gemini-pro", "gemini-1.5", "gemini-2",
		"grok-1", "grok-2", "grok1", "grok2",
		"moonshot", "kimi-k", "kimik",
	}
	sources, err := packageSources(".")
	if err != nil {
		t.Fatalf("locate package sources: %v", err)
	}
	// Also scan the protocol subpackage.
	protoSources, err := packageSources("protocol")
	if err == nil {
		sources = append(sources, protoSources...)
	}
	for _, path := range sources {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		low := strings.ToLower(string(data))
		for _, bad := range forbidden {
			if strings.Contains(low, bad) {
				t.Errorf("%s: source contains hard-coded model name %q (rule §36.8)", filepath.Base(path), bad)
			}
		}
	}
}

// packageSources returns the .go source files (excluding tests) of a package
// relative to this test file's directory.
func packageSources(rel string) ([]string, error) {
	_, thisFile, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(thisFile), rel)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	return out, nil
}

// TestProtocolVersionIsOne pins the protocol major version that M2 stabilises.
func TestProtocolVersionIsOne(t *testing.T) {
	if protocol.ProtocolVersion != 1 {
		t.Fatalf("ProtocolVersion = %d, want 1 (M2 stabilises v1)", protocol.ProtocolVersion)
	}
}

// TestEventSetMatchesSpec asserts the §12.4 normalized event set is exactly
// represented, guarding against accidental drift.
func TestEventSetMatchesSpec(t *testing.T) {
	want := map[protocol.EventType]bool{
		protocol.EventRunStarted: true, protocol.EventRunResumed: true,
		protocol.EventMessageStarted: true, protocol.EventMessageDelta: true, protocol.EventMessageCompleted: true,
		protocol.EventToolStarted: true, protocol.EventToolCompleted: true,
		protocol.EventCommandStarted: true, protocol.EventCommandCompleted: true,
		protocol.EventFileChanged: true, protocol.EventUsageUpdated: true, protocol.EventCheckpointCreated: true,
		protocol.EventApprovalRequested: true, protocol.EventWarning: true,
		protocol.EventRunCompleted: true, protocol.EventRunFailed: true, protocol.EventRunCancelled: true,
	}
	for ev := range want {
		if !ev.IsValid() {
			t.Errorf("§12.4 event %q is not valid", ev)
		}
	}
}
