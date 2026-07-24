package tui

import (
	"strings"
	"testing"
)

func TestM6Screens_NavigateViaPalette(t *testing.T) {
	m := InitialModel()

	// Open palette and navigate to Usage.
	m, _ = Update(m, Msg{Type: MsgKey, Key: "ctrl+p"})
	m.CmdInput = "usage"
	m, eff := Update(m, Msg{Type: MsgKey, Key: "enter"})
	if m.Screen != ScreenUsage {
		t.Errorf("after 'usage' command: screen = %s, want Usage", m.Screen)
	}
	if eff != EffectNone {
		t.Errorf("goto effects should be EffectNone (navigation is inline), got %v", eff)
	}

	// Esc returns to the previous screen.
	prev := m.PrevScreen
	m, _ = Update(m, Msg{Type: MsgKey, Key: "esc"})
	if m.Screen != prev {
		t.Errorf("esc: screen = %s, want %s", m.Screen, prev)
	}
}

func TestM6Screens_QuotasNavigationAndRender(t *testing.T) {
	m := InitialModel()
	m, _ = Update(m, Msg{Type: MsgKey, Key: "ctrl+p"})
	m.CmdInput = "quotas"
	m, _ = Update(m, Msg{Type: MsgKey, Key: "enter"})
	if m.Screen != ScreenQuotas {
		t.Fatalf("screen = %s, want Quotas", m.Screen)
	}
	body := View(m)
	for _, want := range []string{"PROVIDER QUOTAS", "alpha-api", "AVAILABLE", "(exact)"} {
		if !strings.Contains(body, want) {
			t.Errorf("quotas view missing %q:\n%s", want, body)
		}
	}
}

func TestM6Screens_RouteDecisionRender(t *testing.T) {
	m := InitialModel()
	m, _ = Update(m, Msg{Type: MsgKey, Key: "ctrl+p"})
	m.CmdInput = "route"
	m, _ = Update(m, Msg{Type: MsgKey, Key: "enter"})
	if m.Screen != ScreenRouteDecision {
		t.Fatalf("screen = %s, want RouteDecision", m.Screen)
	}
	body := View(m)
	for _, want := range []string{"ROUTE DECISION", "selected:", "FALLBACK CHAIN"} {
		if !strings.Contains(body, want) {
			t.Errorf("route view missing %q:\n%s", want, body)
		}
	}
}

func TestM6Screens_UsageSeparatesIncludedAndPaid(t *testing.T) {
	m := InitialModel()
	m.Screen = ScreenUsage
	body := View(m)
	// AC-18 / §23: included and paid cost must be distinct lines; totals tagged.
	if !strings.Contains(body, "Included cost") || !strings.Contains(body, "Paid API cost") {
		t.Errorf("usage view must separate included vs paid:\n%s", body)
	}
	hasTag := strings.Contains(body, "(estimated)") || strings.Contains(body, "(provider-reported)") || strings.Contains(body, "(exact)")
	if !hasTag {
		t.Errorf("usage total must be confidence-tagged:\n%s", body)
	}
}

func TestM6SnapshotPopulated(t *testing.T) {
	m := InitialModel()
	if m.UsageSummary == nil {
		t.Error("UsageSummary not populated")
	}
	if len(m.QuotaRows) == 0 {
		t.Error("QuotaRows not populated")
	}
	if m.RouteDecision == nil {
		t.Error("RouteDecision not populated")
	}
}

func TestM6ScreensPaletteCommandsPresent(t *testing.T) {
	cmds := AllCommands()
	ids := map[string]bool{}
	for _, c := range cmds {
		ids[c.ID] = true
	}
	for _, want := range []string{"goto-usage", "goto-quotas", "goto-route"} {
		if !ids[want] {
			t.Errorf("palette missing command %q", want)
		}
	}
}
