package bootstrap

import (
	"context"
	"errors"
	"runtime"
	"strings"
)

// Detector abstracts the environment probes the system scan performs (spec §7.2
// stage 1). Production wires a *CommandDetector that shells out to read-only
// commands; tests inject a *FakeDetector that returns canned results. A detector
// MUST be read-only — it never installs or mutates anything.
type Detector interface {
	// LookPath searches for an executable on PATH (like exec.LookPath).
	LookPath(name string) (string, error)
	// Output runs a command and returns its trimmed stdout. Used for --version
	// probes. It MUST be read-only.
	Output(ctx context.Context, name string, args ...string) (string, error)
	// UserShell returns the user's login shell (e.g. /bin/zsh).
	UserShell() string
	// HomeDir returns the user home directory.
	HomeDir() string
	// IsElevated reports whether the process is running with elevated
	// privileges (sudo/admin). Used to warn, never to escalate silently.
	IsElevated() bool
}

// Tool describes one externally-installed tool the scan looks for (§7.2 stage 1).
type Tool struct {
	ID       string // canonical id (e.g. "git", "codex", "docker")
	Category ToolCategory
	// VersionCommand is the read-only command used to detect + version the tool.
	VersionCommand []string
}

// ToolCategory groups tools for the installation plan and the profile defaults.
type ToolCategory string

const (
	CatVCS            ToolCategory = "vcs"
	CatCodingAgent    ToolCategory = "coding_agent"
	CatImageProvider  ToolCategory = "image_provider"
	CatRuntime        ToolCategory = "runtime"   // jdk, node
	CatMobile         ToolCategory = "mobile"    // android sdk
	CatContainer      ToolCategory = "container" // docker, podman
	CatPackageManager ToolCategory = "package_manager"
	CatDaemon         ToolCategory = "daemon" // neuroforge daemon service
)

// DetectedTool is the scan result for one tool.
type DetectedTool struct {
	ID       string
	Category ToolCategory
	Path     string
	Version  string
	Present  bool
}

// SystemScan is the full result of stage 1 (§7.2 stage 1).
type SystemScan struct {
	OS             string // runtime.GOOS
	Arch           string // runtime.GOARCH
	Shell          string
	HomeDir        string
	Elevated       bool
	PackageManager string // detected native package manager (brew/apt/etc.)
	Tools          []DetectedTool
}

// Find returns the detected tool by id, if any.
func (s SystemScan) Find(id string) (DetectedTool, bool) {
	for _, t := range s.Tools {
		if t.ID == id {
			return t, true
		}
	}
	return DetectedTool{}, false
}

// ToolsByCategory returns the detected tools in a category.
func (s SystemScan) ToolsByCategory(c ToolCategory) []DetectedTool {
	var out []DetectedTool
	for _, t := range s.Tools {
		if t.Category == c {
			out = append(out, t)
		}
	}
	return out
}

// DefaultToolSet returns the canonical set of tools the scan probes. This is the
// §7.2 stage-1 checklist. It does NOT hard-code any model names (rule §36.4) —
// coding agents are engines, their models come from the catalog.
func DefaultToolSet() []Tool {
	return []Tool{
		{ID: "git", Category: CatVCS, VersionCommand: []string{"git", "--version"}},
		{ID: "docker", Category: CatContainer, VersionCommand: []string{"docker", "--version"}},
		{ID: "podman", Category: CatContainer, VersionCommand: []string{"podman", "--version"}},
		{ID: "gh", Category: CatVCS, VersionCommand: []string{"gh", "--version"}},
		{ID: "glab", Category: CatVCS, VersionCommand: []string{"glab", "--version"}},
		{ID: "java", Category: CatRuntime, VersionCommand: []string{"java", "-version"}},
		{ID: "node", Category: CatRuntime, VersionCommand: []string{"node", "--version"}},
		{ID: "adb", Category: CatMobile, VersionCommand: []string{"adb", "--version"}},
		// Coding agents (§12): detected, never hard-coded to a model.
		{ID: "codex", Category: CatCodingAgent, VersionCommand: []string{"codex", "--version"}},
		{ID: "claude", Category: CatCodingAgent, VersionCommand: []string{"claude", "--version"}},
		{ID: "gemini", Category: CatCodingAgent, VersionCommand: []string{"gemini", "--version"}},
		{ID: "kimi", Category: CatCodingAgent, VersionCommand: []string{"kimi", "--version"}},
		{ID: "grok", Category: CatCodingAgent, VersionCommand: []string{"grok", "--version"}},
		{ID: "opencode", Category: CatCodingAgent, VersionCommand: []string{"opencode", "--version"}},
	}
}

// Scan performs stage 1 of onboarding (§7.2 stage 1). It is READ-ONLY: it never
// installs, mutates or escalates. It probes each tool in the default set and
// records presence + version.
func Scan(ctx context.Context, d Detector) (SystemScan, error) {
	if d == nil {
		return SystemScan{}, errors.New("bootstrap: nil detector")
	}
	scan := SystemScan{
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		Shell:          d.UserShell(),
		HomeDir:        d.HomeDir(),
		Elevated:       d.IsElevated(),
		PackageManager: detectPackageManager(runtime.GOOS, d),
	}
	for _, tool := range DefaultToolSet() {
		dt := detectTool(ctx, d, tool)
		scan.Tools = append(scan.Tools, dt)
	}
	return scan, nil
}

func detectTool(ctx context.Context, d Detector, tool Tool) DetectedTool {
	dt := DetectedTool{ID: tool.ID, Category: tool.Category}
	path, err := d.LookPath(tool.ID)
	if err != nil || path == "" {
		return dt
	}
	dt.Path = path
	dt.Present = true
	if len(tool.VersionCommand) > 0 {
		if out, err := d.Output(ctx, tool.VersionCommand[0], tool.VersionCommand[1:]...); err == nil {
			dt.Version = normaliseVersion(out)
		}
	}
	return dt
}

// normaliseVersion takes the first useful line of a --version output and trims
// it. This keeps the toolchain lock readable (§7.4).
func normaliseVersion(out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	// First line only.
	if i := strings.IndexByte(out, '\n'); i > 0 {
		out = out[:i]
	}
	return strings.TrimSpace(out)
}

// detectPackageManager guesses the native package manager from the OS + PATH.
func detectPackageManager(goos string, d Detector) string {
	candidates := packageManagersFor(goos)
	for _, name := range candidates {
		if _, err := d.LookPath(name); err == nil {
			return name
		}
	}
	return ""
}

func packageManagersFor(goos string) []string {
	switch goos {
	case "darwin":
		return []string{"brew"}
	case "linux":
		return []string{"apt", "dnf", "yum", "pacman", "apk", "zypper"}
	}
	return nil
}
