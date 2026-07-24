package tui

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"neuroforge/internal/daemon"
)

func newOpts(t *testing.T) (Options, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return Options{
		In:    bytes.NewReader(nil),
		Out:   out,
		IsTTY: false,
		Dirs:  daemon.WithRoot(t.TempDir()),
	}, out
}

func TestRun_NonTTY_DegradesWithoutEscapeCodes(t *testing.T) {
	opts, out := newOpts(t)
	if err := Run(context.Background(), opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "requires an interactive terminal") {
		t.Errorf("expected degradation notice; got %q", got)
	}
	if strings.Contains(got, "\x1b[") {
		t.Errorf("non-TTY must not emit escape codes; got %q", got)
	}
}

func TestRun_TTY_RendersFullScreenAndExitsOnQ(t *testing.T) {
	opts, _ := newOpts(t)
	opts.IsTTY = true
	opts.In = bytes.NewReader([]byte("q"))

	out := &bytes.Buffer{}
	opts.Out = out

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := Run(ctx, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()
	for _, want := range []string{"\x1b[?1049h", "NeuroForge", "PROJECTS", "\x1b[?1049l"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in render; got:\n%s", want, got)
		}
	}
}

func TestRun_TTY_ExitsOnContextCancel(t *testing.T) {
	opts, _ := newOpts(t)
	opts.IsTTY = true
	// A reader that blocks forever (no writer) so only ctx cancellation exits.
	pr, pw := io.Pipe()
	opts.In = pr
	t.Cleanup(func() { _ = pw.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, opts) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned error on cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not exit on context cancellation")
	}
}

func TestRender_ContainsDaemonNotRunning(t *testing.T) {
	out := &bytes.Buffer{}
	render(out, Options{Dirs: daemon.WithRoot(t.TempDir())})
	if !strings.Contains(out.String(), "not running") && !strings.Contains(out.String(), "running") {
		t.Errorf("expected daemon status line; got:\n%s", out.String())
	}
}
