package codex

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func TestDetectMissing(t *testing.T) {
	fr := &fakeRunner{version: "codex 0.42.0"}
	a := newTestAdapter(fr, func(o *Options) {
		o.lookup = func(string) (string, error) {
			return "", errors.New("exec: \"codex\": not found in $PATH")
		}
	})
	d := a.Detect(t.Context())
	if d.Installed {
		t.Errorf("missing codex reported installed: %+v", d)
	}
	if !strings.Contains(d.Detail, "not found") {
		t.Errorf("detail should mention not-found: %s", d.Detail)
	}
}

func TestDetectInstalled(t *testing.T) {
	fr := &fakeRunner{version: "codex 0.42.0"}
	a := newTestAdapter(fr, func(o *Options) {
		o.lookup = func(string) (string, error) { return "/usr/local/bin/codex", nil }
	})
	d := a.Detect(t.Context())
	if !d.Installed {
		t.Fatalf("not installed: %+v", d)
	}
	if d.Path != "/usr/local/bin/codex" {
		t.Errorf("path = %s", d.Path)
	}
	if d.Version != "codex 0.42.0" {
		t.Errorf("version = %s", d.Version)
	}
}

func TestDetectWindowsExtensionsAndShims(t *testing.T) {
	// Detection must tolerate .exe/.cmd/.bat and npm shims; exec.LookPath on
	// Windows honours PATHEXT. We verify the adapter accepts whatever path the
	// resolver returns and probes it.
	for _, tc := range []struct {
		name string
		path string
	}{
		{"exe", `C:\Program Files\Codex\codex.exe`},
		{"cmd", `C:\Users\me\AppData\Roaming\npm\codex.cmd`},
		{"bat", `D:\tools\codex.bat`},
		{"npm-shim-no-ext", `/home/me/.nvm/versions/node/v20/bin/codex`},
		{"powershell", `C:\tools\codex.ps1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRunner{version: "codex 0.13.0"}
			a := newTestAdapter(fr, func(o *Options) {
				o.lookup = func(string) (string, error) { return tc.path, nil }
			})
			d := a.Detect(t.Context())
			if !d.Installed {
				t.Fatalf("%s: not installed: %+v", tc.name, d)
			}
			if d.Path != tc.path {
				t.Errorf("%s: path = %s, want %s", tc.name, d.Path, tc.path)
			}
			// The probe must have been invoked against the resolved path.
			starts := fr.starts()
			if len(starts) == 0 || starts[0][0] != tc.path {
				t.Errorf("%s: probe argv[0] = %v", tc.name, starts)
			}
		})
	}
}

func TestDetectSpacesAndUnicodePath(t *testing.T) {
	path := "/home/mé/My Tools/codex"
	fr := &fakeRunner{version: "codex 1.2.3"}
	a := newTestAdapter(fr, func(o *Options) {
		o.lookup = func(string) (string, error) { return path, nil }
	})
	d := a.Detect(t.Context())
	if !d.Installed {
		t.Fatalf("spaces/unicode path not installed: %+v", d)
	}
	if d.Path != path {
		t.Errorf("path = %s", d.Path)
	}
}

func TestDetectProbeLaunchFailure(t *testing.T) {
	// Binary resolves but cannot be launched (e.g. not executable): the adapter
	// reports not-installed with a diagnostic, never panics.
	fr := &fakeRunner{startErr: errors.New("permission denied")}
	a := newTestAdapter(fr, func(o *Options) {
		o.lookup = func(string) (string, error) { return "/bin/codex", nil }
	})
	d := a.Detect(t.Context())
	if d.Installed {
		t.Errorf("should not be installed when probe fails: %+v", d)
	}
	if !strings.Contains(d.Detail, "could not be launched") {
		t.Errorf("detail should explain launch failure: %s", d.Detail)
	}
}

func TestDetectIsCached(t *testing.T) {
	var starts int32
	fr := &fakeRunner{version: "codex 0.42.0"}
	wrap := func(o *Options) {
		o.lookup = func(string) (string, error) { return "/bin/codex", nil }
		o.runner = startCountingRunner(fr, &starts)
	}
	a := newTestAdapter(fr, wrap)
	_ = a.Detect(t.Context())
	_ = a.Detect(t.Context())
	_ = a.Version(t.Context())
	// Detection probes "codex --version" exactly once across all calls.
	if got := atomic.LoadInt32(&starts); got != 1 {
		t.Errorf("expected exactly 1 probe Start, got %d", got)
	}
}

func TestDetectUnparsableVersionStillInstalled(t *testing.T) {
	fr := &fakeRunner{version: "codex-custom-build-abcdef"}
	a := newTestAdapter(fr, func(o *Options) {
		o.lookup = func(string) (string, error) { return "/bin/codex", nil }
	})
	d := a.Detect(t.Context())
	if !d.Installed {
		t.Errorf("unparsable version should still be installed: %+v", d)
	}
	caps := a.Capabilities(t.Context())
	if caps.HeadlessMode {
		t.Errorf("unparsable version should not claim HeadlessMode: %+v", caps)
	}
}

func TestHealthStatuses(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		fr := &fakeRunner{}
		a := newTestAdapter(fr, func(o *Options) {
			o.lookup = func(string) (string, error) { return "", errors.New("not found") }
		})
		if h := a.Health(t.Context(), protocol.Account{}); h.Status != protocol.HealthDown {
			t.Errorf("missing → %s, want down", h.Status)
		}
	})
	t.Run("ok", func(t *testing.T) {
		fr := &fakeRunner{version: "codex 0.42.0"}
		a := newTestAdapter(fr, func(o *Options) {
			o.lookup = func(string) (string, error) { return "/bin/codex", nil }
		})
		if h := a.Health(t.Context(), protocol.Account{}); h.Status != protocol.HealthOK {
			t.Errorf("installed → %s, want ok", h.Status)
		}
	})
	t.Run("degraded-auth", func(t *testing.T) {
		// A version probe that surfaces an auth signal reports degraded.
		fr := &fakeRunner{
			version: "codex 0.42.0",
			script:  nil,
		}
		// Inject auth signal via the probe stderr by overriding version output to
		// carry it (detection uses stdout; Health scans stdout+stderr+detail).
		fr.version = "codex 0.42.0 not logged in"
		a := newTestAdapter(fr, func(o *Options) {
			o.lookup = func(string) (string, error) { return "/bin/codex", nil }
		})
		h := a.Health(t.Context(), protocol.Account{})
		if h.Status != protocol.HealthDegraded {
			t.Errorf("auth signal → %s (%s), want degraded", h.Status, h.Detail)
		}
	})
}

// countingRunner wraps a Runner and counts Start calls via an atomic.
type countingRunner struct {
	inner Runner
	n     *int32
}

func startCountingRunner(inner Runner, n *int32) *countingRunner {
	return &countingRunner{inner: inner, n: n}
}

func (c *countingRunner) Start(argv []string, dir string, env []string) (Proc, error) {
	atomic.AddInt32(c.n, 1)
	return c.inner.Start(argv, dir, env)
}
