package tui

import (
	"time"

	"neuroforge/internal/transport"
)

// Screen identifies the active TUI pane.
type Screen int

const (
	ScreenProjects Screen = iota
	ScreenTasks
	ScreenProjectDetail
	ScreenTaskDetail
	ScreenHelp
)

func (s Screen) String() string {
	switch s {
	case ScreenProjects:
		return "Projects"
	case ScreenTasks:
		return "Tasks"
	case ScreenProjectDetail:
		return "Project Detail"
	case ScreenTaskDetail:
		return "Task Detail"
	case ScreenHelp:
		return "Help"
	}
	return "?"
}

// Effect is a side-effect the main loop should perform after Update.
type Effect int

const (
	EffectNone Effect = iota
	EffectQuit
	EffectRefreshProjects
	EffectRefreshTasks
	EffectStartProject
	EffectPauseProject
	EffectStopProject
	EffectPauseTask
	EffectCancelTask
	EffectShowTasksForProject
	EffectShowProjectDetail
	EffectShowTaskDetail
	EffectStartProjectAdd
)

// Model is the complete TUI state. It is a plain struct so the update logic
// can be tested without a terminal.
type Model struct {
	Screen          Screen
	PrevScreen      Screen
	Projects        []transport.ProjectDTO
	Tasks           []transport.TaskDTO
	SelectedProject int
	SelectedTask    int
	ActiveProjectID string

	// Command palette
	CmdPaletteOpen bool
	CmdInput       string
	CmdSelected    int

	// Detail view target
	DetailID string

	// Status
	StatusMsg     string
	DaemonRunning bool
	Width         int
	Height        int

	// Scrolling offset for long lists
	ScrollOffset int

	// Last refresh time
	LastRefresh time.Time
}

// Msg is an input event for the Update function.
type Msg struct {
	Type      MsgType
	Key       string
	Projects  []transport.ProjectDTO
	Tasks     []transport.TaskDTO
	Event     transport.Event
	Err       error
	Effect    Effect
	StatusMsg string
}

// MsgType enumerates input event types.
type MsgType int

const (
	MsgInit MsgType = iota
	MsgKey
	MsgProjectsLoaded
	MsgTasksLoaded
	MsgDaemonEvent
	MsgError
	MsgStatus
)

// Command is a command palette entry.
type Command struct {
	ID   string
	Name string
	Desc string
	Run  Effect
}

// AllCommands returns the available commands for the palette.
func AllCommands() []Command {
	return []Command{
		{"refresh-projects", "Refresh projects", "Reload the project list", EffectRefreshProjects},
		{"refresh-tasks", "Refresh tasks", "Reload the task list", EffectRefreshTasks},
		{"goto-projects", "Go to Projects", "Switch to the projects screen", EffectNone},
		{"goto-tasks", "Go to Tasks", "Switch to the tasks screen", EffectNone},
		{"start-project", "Start project", "Start the factory for the selected project", EffectStartProject},
		{"pause-project", "Pause project", "Pause the selected project", EffectPauseProject},
		{"stop-project", "Stop project", "Stop the selected project", EffectStopProject},
		{"pause-task", "Pause task", "Pause the selected task", EffectPauseTask},
		{"cancel-task", "Cancel task", "Cancel the selected task", EffectCancelTask},
		{"add-project", "Add project", "Register a new Git repository", EffectStartProjectAdd},
		{"quit", "Quit", "Exit NeuroForge", EffectQuit},
	}
}

// MatchCommands filters commands by a fuzzy/prefix match on the query.
func MatchCommands(query string) []Command {
	if query == "" {
		return AllCommands()
	}
	var out []Command
	for _, cmd := range AllCommands() {
		if containsFold(cmd.Name, query) || containsFold(cmd.ID, query) {
			out = append(out, cmd)
		}
	}
	return out
}

func containsFold(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			a := s[i+j]
			b := sub[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// InitialModel returns the starting model state.
func InitialModel() Model {
	return Model{
		Screen:         ScreenProjects,
		Projects:       []transport.ProjectDTO{},
		Tasks:          []transport.TaskDTO{},
		CmdPaletteOpen: false,
		DaemonRunning:  false,
		Width:          80,
		Height:         24,
	}
}
