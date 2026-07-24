package tui

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"

	"neuroforge/internal/transport"
)

// ---- Model/Update tests (no terminal required) ----

func TestUpdate_KeyDownMovesCursor(t *testing.T) {
	m := InitialModel()
	m.Projects = []transport.ProjectDTO{
		{ID: "a"}, {ID: "b"}, {ID: "c"},
	}
	m.SelectedProject = 0

	m, _ = Update(m, Msg{Type: MsgKey, Key: "down"})
	if m.SelectedProject != 1 {
		t.Fatalf("down: cursor=%d want 1", m.SelectedProject)
	}

	m, _ = Update(m, Msg{Type: MsgKey, Key: "down"})
	if m.SelectedProject != 2 {
		t.Fatalf("down: cursor=%d want 2", m.SelectedProject)
	}

	// At bottom: should not advance past last item.
	m, _ = Update(m, Msg{Type: MsgKey, Key: "down"})
	if m.SelectedProject != 2 {
		t.Fatalf("down at bottom: cursor=%d want 2", m.SelectedProject)
	}

	m, _ = Update(m, Msg{Type: MsgKey, Key: "up"})
	if m.SelectedProject != 1 {
		t.Fatalf("up: cursor=%d want 1", m.SelectedProject)
	}
}

func TestUpdate_JKKeysWork(t *testing.T) {
	m := InitialModel()
	m.Projects = []transport.ProjectDTO{{ID: "a"}, {ID: "b"}}

	m, _ = Update(m, Msg{Type: MsgKey, Key: "j"})
	if m.SelectedProject != 1 {
		t.Fatalf("j: cursor=%d want 1", m.SelectedProject)
	}
	m, _ = Update(m, Msg{Type: MsgKey, Key: "k"})
	if m.SelectedProject != 0 {
		t.Fatalf("k: cursor=%d want 0", m.SelectedProject)
	}
}

func TestUpdate_TabSwitchesScreens(t *testing.T) {
	m := InitialModel()
	if m.Screen != ScreenProjects {
		t.Fatalf("initial screen=%v want Projects", m.Screen)
	}
	m, _ = Update(m, Msg{Type: MsgKey, Key: "tab"})
	if m.Screen != ScreenTasks {
		t.Fatalf("after tab: screen=%v want Tasks", m.Screen)
	}
	m, _ = Update(m, Msg{Type: MsgKey, Key: "tab"})
	if m.Screen != ScreenProjects {
		t.Fatalf("after second tab: screen=%v want Projects", m.Screen)
	}
}

func TestUpdate_EnterOnProjectOpensTasks(t *testing.T) {
	m := InitialModel()
	m.Projects = []transport.ProjectDTO{{ID: "myapp", Name: "MyApp"}}
	m.SelectedProject = 0

	m, eff := Update(m, Msg{Type: MsgKey, Key: "enter"})
	if m.Screen != ScreenTasks {
		t.Fatalf("screen=%v want Tasks", m.Screen)
	}
	if m.ActiveProjectID != "myapp" {
		t.Fatalf("activeProject=%q want myapp", m.ActiveProjectID)
	}
	if eff != EffectRefreshTasks {
		t.Fatalf("effect=%v want RefreshTasks", eff)
	}
}

func TestUpdate_QuitOnQ(t *testing.T) {
	m := InitialModel()
	_, eff := Update(m, Msg{Type: MsgKey, Key: "q"})
	if eff != EffectQuit {
		t.Fatalf("q: effect=%v want Quit", eff)
	}
}

func TestUpdate_CommandPaletteOpen(t *testing.T) {
	m := InitialModel()
	m, _ = Update(m, Msg{Type: MsgKey, Key: "ctrl+p"})
	if !m.CmdPaletteOpen {
		t.Fatal("ctrl+p should open palette")
	}

	// Type to filter
	m, _ = Update(m, Msg{Type: MsgKey, Key: "r"})
	m, _ = Update(m, Msg{Type: MsgKey, Key: "e"})
	m, _ = Update(m, Msg{Type: MsgKey, Key: "f"})
	if m.CmdInput != "ref" {
		t.Fatalf("input=%q want ref", m.CmdInput)
	}
	matches := MatchCommands(m.CmdInput)
	if len(matches) == 0 {
		t.Fatal("expected matches for 'ref'")
	}
	for _, c := range matches {
		if !strings.Contains(strings.ToLower(c.Name), "ref") {
			t.Errorf("match %q does not contain 'ref'", c.Name)
		}
	}

	// Esc closes palette
	m, _ = Update(m, Msg{Type: MsgKey, Key: "esc"})
	if m.CmdPaletteOpen {
		t.Fatal("esc should close palette")
	}
}

func TestUpdate_CommandPaletteEnterExecutes(t *testing.T) {
	m := InitialModel()
	m, _ = Update(m, Msg{Type: MsgKey, Key: ":"})
	m, _ = Update(m, Msg{Type: MsgKey, Key: "q"})
	m, _ = Update(m, Msg{Type: MsgKey, Key: "u"})
	m, _ = Update(m, Msg{Type: MsgKey, Key: "i"})
	m, eff := Update(m, Msg{Type: MsgKey, Key: "enter"})
	if eff != EffectQuit {
		t.Fatalf("quit command: effect=%v want Quit", eff)
	}
}

func TestUpdate_DaemonEventRefreshesProjects(t *testing.T) {
	m := InitialModel()
	m, eff := Update(m, Msg{Type: MsgDaemonEvent, Event: transport.Event{Type: "project.added"}})
	if eff != EffectRefreshProjects {
		t.Fatalf("project.added: effect=%v want RefreshProjects", eff)
	}
	m, eff = Update(m, Msg{Type: MsgDaemonEvent, Event: transport.Event{Type: "task.created"}})
	if eff != EffectRefreshTasks {
		t.Fatalf("task.created: effect=%v want RefreshTasks", eff)
	}
}

func TestUpdate_ProjectsLoadedClampsCursor(t *testing.T) {
	m := InitialModel()
	m.SelectedProject = 5
	m, _ = Update(m, Msg{Type: MsgProjectsLoaded, Projects: []transport.ProjectDTO{{ID: "a"}}})
	if m.SelectedProject != 0 {
		t.Fatalf("cursor=%d want 0 (clamped)", m.SelectedProject)
	}
}

func TestUpdate_StartProjectEmitsEffect(t *testing.T) {
	m := InitialModel()
	m.Projects = []transport.ProjectDTO{{ID: "a", State: "DISABLED"}}
	m.Screen = ScreenProjects
	_, eff := Update(m, Msg{Type: MsgKey, Key: "s"})
	if eff != EffectShowProjectDetail {
		t.Fatalf("s on project: effect=%v want ShowProjectDetail", eff)
	}
}

func TestView_RendersProjectsScreen(t *testing.T) {
	m := InitialModel()
	m.Projects = []transport.ProjectDTO{
		{ID: "myapp", Name: "My App", State: "IDLE", Profile: "LOCAL_REVIEW"},
	}
	out := View(m)
	for _, want := range []string{"NeuroForge", "PROJECTS", "myapp", "IDLE", "My App"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in view:\n%s", want, out)
		}
	}
}

func TestView_RendersEmptyProjects(t *testing.T) {
	m := InitialModel()
	out := View(m)
	if !strings.Contains(out, "No projects") {
		t.Errorf("expected empty message; got:\n%s", out)
	}
}

func TestView_RendersTasksScreen(t *testing.T) {
	m := InitialModel()
	m.Screen = ScreenTasks
	m.ActiveProjectID = "myapp"
	m.Tasks = []transport.TaskDTO{
		{ID: "myapp-1", Description: "Fix login screen", State: "NEW", Priority: "HIGH"},
	}
	out := View(m)
	for _, want := range []string{"TASKS", "myapp-1", "Fix login screen", "NEW", "HIGH"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in view:\n%s", want, out)
		}
	}
}

func TestView_RendersStatusBar(t *testing.T) {
	m := InitialModel()
	m.DaemonRunning = true
	out := View(m)
	if !strings.Contains(out, "daemon running") {
		t.Errorf("expected daemon status in status bar; got:\n%s", out)
	}
}

func TestView_RendersCommandPalette(t *testing.T) {
	m := InitialModel()
	m.CmdPaletteOpen = true
	m.CmdInput = "goto"
	out := View(m)
	if !strings.Contains(out, "Command Palette") {
		t.Errorf("expected command palette; got:\n%s", out)
	}
}

func TestMatchCommands_EmptyReturnsAll(t *testing.T) {
	all := MatchCommands("")
	if len(all) != len(AllCommands()) {
		t.Fatalf("empty query: got %d, want %d", len(all), len(AllCommands()))
	}
}

func TestMatchCommands_FiltersByQuery(t *testing.T) {
	matches := MatchCommands("quit")
	if len(matches) != 1 {
		t.Fatalf("quit: got %d matches, want 1", len(matches))
	}
	if matches[0].ID != "quit" {
		t.Fatalf("match ID=%q want quit", matches[0].ID)
	}
}

func TestParseKey_Printable(t *testing.T) {
	reader := bufio.NewReader(bytes.NewReader([]byte{}))
	if got := parseKey('a', reader); got != "a" {
		t.Fatalf("parseKey('a')=%q want 'a'", got)
	}
}

func TestParseKey_Enter(t *testing.T) {
	if got := parseKey(0x0d, nil); got != "enter" {
		t.Fatalf("parseKey(0x0d)=%q want 'enter'", got)
	}
}

func TestParseKey_CtrlC(t *testing.T) {
	if got := parseKey(0x03, nil); got != "ctrl+c" {
		t.Fatalf("parseKey(0x03)=%q want 'ctrl+c'", got)
	}
}

func TestParseKey_Tab(t *testing.T) {
	if got := parseKey(0x09, nil); got != "tab" {
		t.Fatalf("parseKey(0x09)=%q want 'tab'", got)
	}
}

// ---- Non-TTY Run test ----

func TestRun_NonTTY_DegradesWithoutEscapeCodes(t *testing.T) {
	out := &bytes.Buffer{}
	opts := Options{
		In:    bytes.NewReader(nil),
		Out:   out,
		IsTTY: false,
	}
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
