package conformance

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/fake"
	"neuroforge/internal/adapter/codingagent/plugin"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// in-process fake adapter factory.
func inProcessFactory(ctx context.Context, scenario fake.Scenario) (codingagent.Adapter, func(), error) {
	a := fake.New(fake.AdapterOptions{Scenario: scenario, Installed: true})
	return a, func() {}, nil
}

// plugin factory: spawns the fake-coding-agent jsonrpc plugin per scenario.
var (
	pluginBuildOnce sync.Once
	pluginBin       string
)

func fakePluginBin(t *testing.T) string {
	t.Helper()
	pluginBuildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "fca-conf-*")
		if err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(dir, "fake-coding-agent")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		root := moduleRoot(t)
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/fake-coding-agent")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build: %v\n%s", err, out)
		}
		pluginBin = bin
	})
	if pluginBin == "" {
		t.Fatal("binary not built")
	}
	return pluginBin
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("go.mod not found")
	return ""
}

func pluginFactoryFor(t *testing.T) AdapterFactory {
	bin := fakePluginBin(t)
	return func(ctx context.Context, scenario fake.Scenario) (codingagent.Adapter, func(), error) {
		env := []string{"FAKE_SCENARIO=" + string(scenario)}
		if v, ok := os.LookupEnv("PATH"); ok {
			env = append(env, "PATH="+v)
		}
		dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		ad, err := plugin.DialAdapter(dialCtx, bin, []string{"--mode", "jsonrpc"}, env)
		cancel()
		if err != nil {
			return nil, nil, err
		}
		return ad, func() { _ = ad.(*plugin.Adapter).Close() }, nil
	}
}

func runSuite(t *testing.T, factory AdapterFactory) []CheckResult {
	t.Helper()
	s := &Suite{Factory: factory, Timeout: 15 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	return s.Run(ctx)
}

func TestSuiteAgainstInProcessFake(t *testing.T) {
	results := runSuite(t, inProcessFactory)
	assertAllPass(t, results)
}

func TestSuiteAgainstFakePlugin(t *testing.T) {
	results := runSuite(t, pluginFactoryFor(t))
	assertAllPass(t, results)
}

func assertAllPass(t *testing.T, results []CheckResult) {
	t.Helper()
	passed, total := Summary(results)
	for _, r := range results {
		status := "PASS"
		if !r.Passed {
			status = "FAIL"
		}
		t.Logf("[%s] %s — %s", status, r.Name, r.Detail)
	}
	if passed != total {
		t.Fatalf("conformance: %d/%d checks passed", passed, total)
	}
}

func TestNamesStable(t *testing.T) {
	names := Names()
	want := []string{
		"handshake", "version_compatibility", "event_ordering", "malformed_output",
		"cancellation", "timeout", "quota_failure", "resume", "process_crash",
	}
	if len(names) != len(want) {
		t.Fatalf("Names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("Names[%d] = %s, want %s", i, names[i], want[i])
		}
	}
}

func TestResultFormatting(t *testing.T) {
	r := CheckResult{Name: "x", Passed: true, Detail: "ok"}
	if r.Name != "x" {
		t.Fatal()
	}
	_ = fmt.Sprintf("%v", protocol.EventRunStarted) // keep import
}
