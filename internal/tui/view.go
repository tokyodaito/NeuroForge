package tui

import (
	"fmt"
	"strings"
	"time"

	"neuroforge/internal/transport"
	"neuroforge/internal/version"
)

// View renders the model to a string for terminal output. It uses ANSI escape
// codes for styling. The caller handles entering/leaving the alt screen buffer.
func View(m Model) string {
	var b strings.Builder

	b.WriteString("\x1b[H\x1b[2J") // home + clear
	b.WriteString(header(m))
	b.WriteString("\n\n")

	switch m.Screen {
	case ScreenProjects:
		b.WriteString(projectsView(m))
	case ScreenTasks:
		b.WriteString(tasksView(m))
	case ScreenProjectDetail:
		b.WriteString(projectDetailView(m))
	case ScreenTaskDetail:
		b.WriteString(taskDetailView(m))
	case ScreenUsage:
		b.WriteString(usageView(m))
	case ScreenQuotas:
		b.WriteString(quotasView(m))
	case ScreenRouteDecision:
		b.WriteString(routeDecisionView(m))
	case ScreenHelp:
		b.WriteString(helpView())
	}

	b.WriteString("\n\n")
	b.WriteString(statusBar(m))

	if m.CmdPaletteOpen {
		b.WriteString("\n")
		b.WriteString(commandPaletteView(m))
	}

	return b.String()
}

func header(m Model) string {
	v := version.Current()
	clock := time.Now().Format("15:04")
	title := bold("NeuroForge") + "  " + dim(v.Version) + "  " + dim(clock)
	screen := dim(" | ") + bold(m.Screen.String())
	return title + screen
}

func projectsView(m Model) string {
	var b strings.Builder
	b.WriteString(bold("PROJECTS") + "\n\n")

	if len(m.Projects) == 0 {
		b.WriteString(dim("  No projects registered.") + "\n")
		b.WriteString(dim("  Press 'a' to add a project, or use 'forge project add <path>'") + "\n")
		return b.String()
	}

	b.WriteString(fmt.Sprintf("%-2s %-20s %-12s %-10s %s\n", "", "ID", "STATE", "PROFILE", "NAME"))
	b.WriteString(dim(strings.Repeat("-", 72)) + "\n")

	for i, p := range m.Projects {
		cursor := " "
		style := ""
		if i == m.SelectedProject {
			cursor = ">"
			style = "\x1b[7m" // reverse video
		}
		stateCol := stateColor(p.State)
		line := fmt.Sprintf("%s %s%-20s %-12s %-10s%s %s",
			cursor, style, p.ID, stateCol+p.State+"\x1b[0m", p.Profile, "\x1b[0m", p.Name)
		b.WriteString(line + "\n")
	}
	return b.String()
}

func tasksView(m Model) string {
	var b strings.Builder
	label := bold("TASKS")
	if m.ActiveProjectID != "" {
		label += dim("  [" + m.ActiveProjectID + "]")
	}
	b.WriteString(label + "\n\n")

	if len(m.Tasks) == 0 {
		b.WriteString(dim("  No tasks.") + "\n")
		b.WriteString(dim("  Use 'forge task add -p "+m.ActiveProjectID+" \"description\"'") + "\n")
		return b.String()
	}

	b.WriteString(fmt.Sprintf("%-2s %-16s %-12s %-10s %s\n", "", "ID", "STATE", "PRIORITY", "DESCRIPTION"))
	b.WriteString(dim(strings.Repeat("-", 72)) + "\n")

	for i, t := range m.Tasks {
		cursor := " "
		style := ""
		if i == m.SelectedTask {
			cursor = ">"
			style = "\x1b[7m"
		}
		desc := t.Title
		if desc == "" {
			if len(t.Description) > 40 {
				desc = t.Description[:40] + "..."
			} else {
				desc = t.Description
			}
		}
		stateCol := taskStateColor(t.State)
		line := fmt.Sprintf("%s %s%-16s %s%-12s\x1b[0m %-10s %s",
			cursor, style, t.ID, stateCol, t.State, t.Priority, desc)
		if style != "" {
			line += "\x1b[0m"
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func projectDetailView(m Model) string {
	var b strings.Builder
	b.WriteString(bold("PROJECT DETAIL") + "\n\n")

	var p *transport.ProjectDTO
	for i := range m.Projects {
		if m.Projects[i].ID == m.DetailID {
			p = &m.Projects[i]
			break
		}
	}
	if p == nil {
		b.WriteString(dim("  Project not found: "+m.DetailID) + "\n")
		return b.String()
	}

	stateCol := stateColor(p.State)
	b.WriteString(fmt.Sprintf("  ID:       %s\n", p.ID))
	b.WriteString(fmt.Sprintf("  Name:     %s\n", p.Name))
	b.WriteString(fmt.Sprintf("  Path:     %s\n", p.Path))
	b.WriteString(fmt.Sprintf("  Remote:   %s\n", p.Remote))
	b.WriteString(fmt.Sprintf("  State:    %s%s\x1b[0m\n", stateCol, p.State))
	b.WriteString(fmt.Sprintf("  Profile:  %s\n", p.Profile))
	b.WriteString(fmt.Sprintf("  Created:  %s\n", p.CreatedAt))
	b.WriteString("\n")
	b.WriteString(dim("  Actions: ") + key("s") + dim(" start  ") + key("p") + dim(" pause  ") +
		key("x") + dim(" stop  ") + key("t") + dim(" tasks  ") + key("Esc") + dim(" back"))
	b.WriteString("\n")
	return b.String()
}

func taskDetailView(m Model) string {
	var b strings.Builder
	b.WriteString(bold("TASK DETAIL") + "\n\n")

	var t *transport.TaskDTO
	for i := range m.Tasks {
		if m.Tasks[i].ID == m.DetailID {
			t = &m.Tasks[i]
			break
		}
	}
	if t == nil {
		b.WriteString(dim("  Task not found: "+m.DetailID) + "\n")
		return b.String()
	}

	stateCol := taskStateColor(t.State)
	b.WriteString(fmt.Sprintf("  ID:          %s\n", t.ID))
	b.WriteString(fmt.Sprintf("  Project:     %s\n", t.ProjectID))
	if t.Title != "" {
		b.WriteString(fmt.Sprintf("  Title:       %s\n", t.Title))
	}
	b.WriteString(fmt.Sprintf("  Description: %s\n", t.Description))
	b.WriteString(fmt.Sprintf("  Priority:    %s\n", t.Priority))
	b.WriteString(fmt.Sprintf("  State:       %s%s\x1b[0m\n", stateCol, t.State))
	b.WriteString(fmt.Sprintf("  Created:     %s\n", t.CreatedAt))
	if len(t.Attachments) > 0 {
		b.WriteString("  Attachments:\n")
		for _, a := range t.Attachments {
			b.WriteString(fmt.Sprintf("    - %s (%s, %d bytes)\n", a.Filename, a.MimeType, a.Size))
		}
	}
	b.WriteString("\n")
	b.WriteString(dim("  Actions: ") + key("p") + dim(" pause  ") + key("c") + dim(" cancel  ") +
		key("Esc") + dim(" back"))
	b.WriteString("\n")
	return b.String()
}

func helpView() string {
	var b strings.Builder
	b.WriteString(bold("HELP — Key Bindings") + "\n\n")
	b.WriteString("  " + key("↑/k") + " " + key("↓/j") + "    Navigate list\n")
	b.WriteString("  " + key("Tab") + "         Switch between Projects and Tasks\n")
	b.WriteString("  " + key("Enter") + "     Open detail / select\n")
	b.WriteString("  " + key("s") + "          Show detail (projects)\n")
	b.WriteString("  " + key("r") + "          Refresh current list\n")
	b.WriteString("  " + key("a") + "          Add project (prompt)\n")
	b.WriteString("  " + key("p") + "          Pause project/task\n")
	b.WriteString("  " + key("x") + "          Stop project\n")
	b.WriteString("  " + key("c") + "          Cancel task\n")
	b.WriteString("  " + key("Ctrl+P") + " / " + key(":") + "  Open command palette\n")
	b.WriteString("  " + key("?") + "          Toggle this help\n")
	b.WriteString("  " + key("Esc") + "        Back / close palette\n")
	b.WriteString("  " + key("q") + "          Quit\n")
	b.WriteString("\n")
	b.WriteString(dim("  Mouse: click to select items (if supported by terminal)") + "\n")
	return b.String()
}

func statusBar(m Model) string {
	var b strings.Builder
	b.WriteString(strings.Repeat("-", 72))
	b.WriteString("\n")

	left := ""
	if m.DaemonRunning {
		left += ok("● daemon running")
	} else {
		left += warn("○ daemon not running")
	}

	right := dim(fmt.Sprintf("%d projects  %d tasks", len(m.Projects), len(m.Tasks)))
	b.WriteString(fmt.Sprintf("%s  %s", left, padRight(right, 72-len(left)+len(right))))

	if m.StatusMsg != "" {
		b.WriteString("\n" + warn(m.StatusMsg))
	}

	hints := dim("keys: ") + key("Tab") + dim(" switch  ") +
		key("r") + dim(" refresh  ") + key("Ctrl+P") + dim(" palette  ") +
		key("?") + dim(" help  ") + key("q") + dim(" quit")
	b.WriteString("\n" + hints)
	return b.String()
}

func commandPaletteView(m Model) string {
	var b strings.Builder
	matches := MatchCommands(m.CmdInput)

	b.WriteString("\x1b[7m " + bold("Command Palette") + " \x1b[0m ")
	b.WriteString(m.CmdInput + "\x1b[5m_\x1b[0m\n")
	b.WriteString(dim(strings.Repeat("-", 72)) + "\n")

	maxShow := 8
	if len(matches) == 0 {
		b.WriteString(dim("  No matching commands") + "\n")
	} else {
		for i, cmd := range matches {
			if i >= maxShow {
				b.WriteString(dim(fmt.Sprintf("  ... and %d more", len(matches)-maxShow)) + "\n")
				break
			}
			cursor := "  "
			style := ""
			if i == m.CmdSelected {
				cursor = "> "
				style = "\x1b[7m"
			}
			b.WriteString(fmt.Sprintf("%s%s%-24s%s %s\n", cursor, style, cmd.Name, "\x1b[0m", dim(cmd.Desc)))
		}
	}
	b.WriteString(dim("  Enter to execute  ·  Esc to close") + "\n")
	return b.String()
}

// ---- helpers ----

func stateColor(state string) string {
	switch state {
	case "IDLE":
		return "\x1b[32m" // green
	case "RUNNING":
		return "\x1b[36m" // cyan
	case "PAUSED":
		return "\x1b[33m" // yellow
	case "DRAINING":
		return "\x1b[35m" // magenta
	case "ERROR":
		return "\x1b[31m" // red
	case "DISABLED":
		return "\x1b[2m" // dim
	}
	return ""
}

func taskStateColor(state string) string {
	switch state {
	case "NEW", "INGESTED":
		return "\x1b[36m" // cyan
	case "PAUSED":
		return "\x1b[33m" // yellow
	case "CANCELLED":
		return "\x1b[31m" // red
	}
	return ""
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

func bold(s string) string { return "\x1b[1m" + s + "\x1b[0m" }
func dim(s string) string  { return "\x1b[2m" + s + "\x1b[0m" }
func ok(s string) string   { return "\x1b[32m" + s + "\x1b[0m" }
func warn(s string) string { return "\x1b[33m" + s + "\x1b[0m" }
func key(s string) string  { return "\x1b[7m " + s + " \x1b[0m" }
