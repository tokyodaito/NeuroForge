package tui

import "neuroforge/internal/transport"

// Update processes a message and returns the updated model plus an optional
// effect for the main loop to execute. This function is pure (no I/O) so it
// can be unit-tested directly.
func Update(m Model, msg Msg) (Model, Effect) {
	switch msg.Type {
	case MsgInit:
		return m, EffectRefreshProjects

	case MsgProjectsLoaded:
		m.Projects = msg.Projects
		m.LastRefresh = now()
		// Clamp cursor
		if m.SelectedProject >= len(m.Projects) && len(m.Projects) > 0 {
			m.SelectedProject = len(m.Projects) - 1
		}
		if len(m.Projects) == 0 {
			m.SelectedProject = 0
		}
		return m, EffectNone

	case MsgTasksLoaded:
		m.Tasks = msg.Tasks
		m.LastRefresh = now()
		if m.SelectedTask >= len(m.Tasks) && len(m.Tasks) > 0 {
			m.SelectedTask = len(m.Tasks) - 1
		}
		if len(m.Tasks) == 0 {
			m.SelectedTask = 0
		}
		return m, EffectNone

	case MsgDaemonEvent:
		return handleDaemonEvent(m, msg.Event)

	case MsgStatus:
		m.StatusMsg = msg.StatusMsg
		return m, EffectNone

	case MsgError:
		m.StatusMsg = "Error: " + msg.Err.Error()
		return m, EffectNone

	case MsgKey:
		return handleKey(m, msg.Key)
	}

	return m, EffectNone
}

// handleKey processes a keyboard event.
func handleKey(m Model, key string) (Model, Effect) {
	// Command palette takes priority when open.
	if m.CmdPaletteOpen {
		return handlePaletteKey(m, key)
	}

	// Global keys
	switch key {
	case "q", "ctrl+c":
		return m, EffectQuit
	case "ctrl+p", ":":
		m.CmdPaletteOpen = true
		m.CmdInput = ""
		m.CmdSelected = 0
		return m, EffectNone
	case "tab":
		return switchScreen(m), EffectNone
	case "shift+tab":
		return switchScreenBack(m), EffectNone
	case "r":
		if m.Screen == ScreenProjects {
			return m, EffectRefreshProjects
		}
		return m, EffectRefreshTasks
	case "?":
		m.PrevScreen = m.Screen
		m.Screen = ScreenHelp
		return m, EffectNone
	case "esc":
		if m.Screen == ScreenHelp || m.Screen == ScreenProjectDetail || m.Screen == ScreenTaskDetail {
			m.Screen = m.PrevScreen
			return m, EffectNone
		}
		return m, EffectQuit
	}

	// Screen-specific keys
	switch m.Screen {
	case ScreenProjects:
		return handleProjectsKey(m, key)
	case ScreenTasks:
		return handleTasksKey(m, key)
	case ScreenProjectDetail:
		return handleProjectDetailKey(m, key)
	case ScreenTaskDetail:
		return handleTaskDetailKey(m, key)
	case ScreenHelp:
		if key == "enter" || key == "esc" || key == "q" {
			m.Screen = m.PrevScreen
		}
		return m, EffectNone
	}

	return m, EffectNone
}

func handleProjectsKey(m Model, key string) (Model, Effect) {
	switch key {
	case "up", "k":
		if m.SelectedProject > 0 {
			m.SelectedProject--
		}
		return m, EffectNone
	case "down", "j":
		if m.SelectedProject < len(m.Projects)-1 {
			m.SelectedProject++
		}
		return m, EffectNone
	case "enter":
		if len(m.Projects) > 0 {
			p := m.Projects[m.SelectedProject]
			m.ActiveProjectID = p.ID
			m.Screen = ScreenTasks
			m.SelectedTask = 0
			return m, EffectRefreshTasks
		}
		return m, EffectNone
	case "s":
		if len(m.Projects) > 0 {
			m.DetailID = m.Projects[m.SelectedProject].ID
			m.PrevScreen = m.Screen
			m.Screen = ScreenProjectDetail
			return m, EffectShowProjectDetail
		}
	case "a":
		return m, EffectStartProjectAdd
	case "x":
		if len(m.Projects) > 0 {
			m.DetailID = m.Projects[m.SelectedProject].ID
			m.PrevScreen = m.Screen
			m.Screen = ScreenProjectDetail
			return m, EffectShowProjectDetail
		}
	}
	return m, EffectNone
}

func handleTasksKey(m Model, key string) (Model, Effect) {
	switch key {
	case "up", "k":
		if m.SelectedTask > 0 {
			m.SelectedTask--
		}
		return m, EffectNone
	case "down", "j":
		if m.SelectedTask < len(m.Tasks)-1 {
			m.SelectedTask++
		}
		return m, EffectNone
	case "enter":
		if len(m.Tasks) > 0 {
			m.DetailID = m.Tasks[m.SelectedTask].ID
			m.PrevScreen = m.Screen
			m.Screen = ScreenTaskDetail
			return m, EffectShowTaskDetail
		}
		return m, EffectNone
	case "p":
		if len(m.Tasks) > 0 {
			return m, EffectPauseTask
		}
	case "c":
		if len(m.Tasks) > 0 {
			return m, EffectCancelTask
		}
	}
	return m, EffectNone
}

func handleProjectDetailKey(m Model, key string) (Model, Effect) {
	switch key {
	case "s":
		return m, EffectStartProject
	case "p":
		return m, EffectPauseProject
	case "x":
		return m, EffectStopProject
	case "t":
		m.Screen = ScreenTasks
		return m, EffectRefreshTasks
	}
	return m, EffectNone
}

func handleTaskDetailKey(m Model, key string) (Model, Effect) {
	switch key {
	case "p":
		return m, EffectPauseTask
	case "c":
		return m, EffectCancelTask
	}
	return m, EffectNone
}

func handlePaletteKey(m Model, key string) (Model, Effect) {
	switch key {
	case "esc", "ctrl+c":
		m.CmdPaletteOpen = false
		m.CmdInput = ""
		return m, EffectNone
	case "enter":
		matches := MatchCommands(m.CmdInput)
		if m.CmdSelected >= 0 && m.CmdSelected < len(matches) {
			cmd := matches[m.CmdSelected]
			m.CmdPaletteOpen = false
			m.CmdInput = ""
			if cmd.Run == EffectQuit {
				return m, EffectQuit
			}
			return applyCommand(m, cmd)
		}
		m.CmdPaletteOpen = false
		return m, EffectNone
	case "up":
		if m.CmdSelected > 0 {
			m.CmdSelected--
		}
		return m, EffectNone
	case "down":
		matches := MatchCommands(m.CmdInput)
		if m.CmdSelected < len(matches)-1 {
			m.CmdSelected++
		}
		return m, EffectNone
	case "backspace":
		if len(m.CmdInput) > 0 {
			m.CmdInput = m.CmdInput[:len(m.CmdInput)-1]
			m.CmdSelected = 0
		}
		return m, EffectNone
	default:
		// Printable character
		if len(key) == 1 && key[0] >= 0x20 && key[0] <= 0x7e {
			m.CmdInput += key
			m.CmdSelected = 0
		}
		return m, EffectNone
	}
}

func applyCommand(m Model, cmd Command) (Model, Effect) {
	switch cmd.ID {
	case "goto-projects":
		m.Screen = ScreenProjects
		return m, EffectRefreshProjects
	case "goto-tasks":
		m.Screen = ScreenTasks
		return m, EffectRefreshTasks
	case "refresh-projects":
		return m, EffectRefreshProjects
	case "refresh-tasks":
		return m, EffectRefreshTasks
	case "start-project":
		return m, EffectStartProject
	case "pause-project":
		return m, EffectPauseProject
	case "stop-project":
		return m, EffectStopProject
	case "pause-task":
		return m, EffectPauseTask
	case "cancel-task":
		return m, EffectCancelTask
	case "add-project":
		return m, EffectStartProjectAdd
	}
	return m, EffectNone
}

func handleDaemonEvent(m Model, evt transport.Event) (Model, Effect) {
	switch evt.Type {
	case "project.added", "project.removed", "project.state_changed":
		return m, EffectRefreshProjects
	case "task.created", "task.state_changed":
		return m, EffectRefreshTasks
	case "daemon.started":
		m.DaemonRunning = true
		return m, EffectRefreshProjects
	case "daemon.stopped":
		m.DaemonRunning = false
		m.StatusMsg = "Daemon stopped"
		return m, EffectNone
	}
	return m, EffectNone
}

func switchScreen(m Model) Model {
	switch m.Screen {
	case ScreenProjects:
		m.Screen = ScreenTasks
	case ScreenTasks:
		m.Screen = ScreenProjects
	case ScreenProjectDetail:
		m.Screen = ScreenTasks
	case ScreenTaskDetail:
		m.Screen = ScreenProjects
	case ScreenHelp:
		m.Screen = m.PrevScreen
	}
	return m
}

func switchScreenBack(m Model) Model {
	return switchScreen(m) // bidirectional for now
}
