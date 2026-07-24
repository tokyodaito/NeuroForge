package cli

import (
	"strings"
	"testing"
)

func TestDoctor_FreshHome_ExitOK(t *testing.T) {
	a, out, _ := newTestApp(t)
	if code := a.Run([]string{"doctor"}); code != ExitOK {
		t.Fatalf("code = %d, want %d", code, ExitOK)
	}
	got := out.String()
	for _, want := range []string{"forge-version", "platform", "runtime-home", "database", "daemon", "OK"} {
		if !strings.Contains(got, want) {
			t.Errorf("doctor output missing %q; got:\n%s", want, got)
		}
	}
}

func TestDoctor_JSON(t *testing.T) {
	a, out, _ := newTestApp(t)
	if code := a.Run([]string{"doctor", "--json"}); code != ExitOK {
		t.Fatalf("code = %d, want %d", code, ExitOK)
	}
	got := out.String()
	if !strings.HasPrefix(strings.TrimSpace(got), "{") {
		t.Errorf("expected JSON object; got %q", got)
	}
	if !strings.Contains(got, `"checks"`) {
		t.Errorf("expected checks key; got %q", got)
	}
}

func TestDoctor_Help(t *testing.T) {
	a, out, _ := newTestApp(t)
	if code := a.Run([]string{"doctor", "-h"}); code != ExitOK {
		t.Fatalf("code = %d, want %d", code, ExitOK)
	}
	if !strings.Contains(out.String(), "Usage") {
		t.Errorf("expected usage; got %q", out.String())
	}
}
