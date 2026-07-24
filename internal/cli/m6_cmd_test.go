package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func newAppCapture() (*App, *bytes.Buffer, *bytes.Buffer) {
	out := &bytes.Buffer{}
	err := &bytes.Buffer{}
	a := &App{Name: "forge", Out: out, Err: err, Stdin: strings.NewReader("")}
	return a, out, err
}

func TestRouteExplain_TextAndJSON(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		a, out, _ := newAppCapture()
		code := a.Run([]string{"route", "explain", "fix typo in README"})
		if code != ExitOK {
			t.Fatalf("exit %d", code)
		}
		body := out.String()
		for _, want := range []string{"ROUTE DECISION", "selected:", "TINY"} {
			if !strings.Contains(body, want) {
				t.Errorf("output missing %q:\n%s", want, body)
			}
		}
	})

	t.Run("json", func(t *testing.T) {
		a, out, _ := newAppCapture()
		code := a.Run([]string{"route", "explain", "--complexity", "C4", "--json", "rewrite the design system"})
		if code != ExitOK {
			t.Fatalf("exit %d", code)
		}
		var got routeExplainJSON
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("invalid JSON: %v\n%s", err, out.String())
		}
		if got.TargetTier != "FRONTIER" && got.TargetTier != "HEAVY" {
			t.Errorf("C4 target tier = %s, want HEAVY/FRONTIER", got.TargetTier)
		}
		if got.Selected.Engine == "" {
			t.Error("no selected route in JSON")
		}
		if len(got.Alternatives) == 0 {
			t.Error("no alternatives in JSON")
		}
		if len(got.FallbackChain) < 2 {
			t.Errorf("fallback chain len %d, want >= 2", len(got.FallbackChain))
		}
	})

	t.Run("invalid complexity rejected", func(t *testing.T) {
		a, _, errBuf := newAppCapture()
		code := a.Run([]string{"route", "explain", "--complexity", "C9"})
		if code != ExitErr {
			t.Fatalf("expected exit error, got %d", code)
		}
		if !strings.Contains(errBuf.String(), "invalid --complexity") {
			t.Errorf("stderr = %q", errBuf.String())
		}
	})
}

func TestQuotaCommand(t *testing.T) {
	a, out, _ := newAppCapture()
	code := a.Run([]string{"quota"})
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	body := out.String()
	for _, want := range []string{"PROVIDER QUOTA", "alpha-api", "AVAILABLE", "(exact)"} {
		if !strings.Contains(body, want) {
			t.Errorf("output missing %q:\n%s", want, body)
		}
	}

	t.Run("json", func(t *testing.T) {
		a, out, _ := newAppCapture()
		if a.Run([]string{"quota", "--json"}) != ExitOK {
			t.Fatal("exit error")
		}
		var got []quotaJSON
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if len(got) == 0 {
			t.Fatal("no quota rows")
		}
		for _, q := range got {
			if q.Confidence == "" || q.State == "" {
				t.Errorf("incomplete quota row: %+v", q)
			}
		}
	})
}

func TestUsageCommand_IncludedVsPaidSeparate(t *testing.T) {
	a, out, _ := newAppCapture()
	if a.Run([]string{"usage"}) != ExitOK {
		t.Fatal("exit error")
	}
	body := out.String()
	// Included and paid cost must be shown as distinct lines (§23).
	if !strings.Contains(body, "Included cost") || !strings.Contains(body, "Paid API cost") {
		t.Errorf("usage must separate included from paid cost:\n%s", body)
	}
	// Estimated total must carry a confidence tag (AC-18).
	if !strings.Contains(body, "(estimated)") && !strings.Contains(body, "(provider-reported)") && !strings.Contains(body, "(exact)") {
		t.Errorf("usage total must be confidence-tagged:\n%s", body)
	}
}

func TestCostCommand(t *testing.T) {
	a, out, _ := newAppCapture()
	if a.Run([]string{"cost"}) != ExitOK {
		t.Fatal("exit error")
	}
	body := out.String()
	for _, want := range []string{"COST REPORT", "Today", "This month", "Included (subscription)", "Paid API"} {
		if !strings.Contains(body, want) {
			t.Errorf("output missing %q:\n%s", want, body)
		}
	}
}

func TestRouteHelpText(t *testing.T) {
	a, out, _ := newAppCapture()
	a.Run([]string{"route", "--help"})
	if !strings.Contains(out.String(), "forge route explain") {
		t.Errorf("route help missing usage: %s", out.String())
	}
}
