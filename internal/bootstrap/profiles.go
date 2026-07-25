package bootstrap

import "sort"

// Profile is an onboarding installation profile (spec §7.2 stage 2).
type Profile string

const (
	ProfileMinimal  Profile = "minimal"
	ProfileStandard Profile = "standard"
	ProfileAndroid  Profile = "android"
	ProfileWeb      Profile = "web"
	ProfileFull     Profile = "full"
	ProfileCustom   Profile = "custom"
)

// IsValid reports whether p is a known profile.
func (p Profile) IsValid() bool {
	switch p {
	case ProfileMinimal, ProfileStandard, ProfileAndroid, ProfileWeb, ProfileFull, ProfileCustom:
		return true
	}
	return false
}

// String returns the profile identifier.
func (p Profile) String() string { return string(p) }

// AllProfiles returns the profiles in canonical order (§7.2 stage 2).
func AllProfiles() []Profile {
	return []Profile{ProfileMinimal, ProfileStandard, ProfileAndroid, ProfileWeb, ProfileFull, ProfileCustom}
}

// ProfileSpec describes which tools a profile requests to install. Each entry
// carries the tool id, whether a sudo/system step is required to install it, and
// whether it touches the shell profile (e.g. PATH additions).
type ProfileSpec struct {
	Profile Profile
	Tools   []ToolRequest
}

// ToolRequest is one tool a profile wants installed.
type ToolRequest struct {
	ID                 string
	Category           ToolCategory
	NeedsSudo          bool   // §36.18: flagged, never silent
	ShellProfileChange string // non-empty ⇒ a PATH/env line would be added (shown as diff, §7.2 stage 4)
	InstallHint        string // human-readable install method (e.g. "brew install git")
}

// ProfileSpecFor returns the canonical tool requests for a profile. CUSTOM is
// empty by default (the user picks); the plan then merges the custom selection.
func ProfileSpecFor(p Profile) ProfileSpec {
	switch p {
	case ProfileMinimal:
		// Bare minimum: git + one coding agent + the daemon. No containers, no
		// mobile runtimes, no image providers. The cheapest safe start.
		return ProfileSpec{Profile: p, Tools: []ToolRequest{
			mustHave("git", CatVCS, "brew install git"),
			codingAgent("codex", "npm install -g @openai/codex"),
			{ID: "neuroforge-daemon", Category: CatDaemon, ShellProfileChange: "", InstallHint: "started as a user service"},
		}}
	case ProfileStandard:
		// Git + all coding agents + GitHub CLI + daemon.
		tools := []ToolRequest{
			mustHave("git", CatVCS, "brew install git"),
			mustHave("gh", CatVCS, "brew install gh"),
			codingAgent("codex", "npm install -g @openai/codex"),
			codingAgent("claude", "npm install -g @anthropic-ai/claude-code"),
			codingAgent("gemini", "npm install -g @anthropic-ai/gemini-cli"),
			codingAgent("kimi", "npm install -g @kimi/kimi-code"),
			codingAgent("grok", "npm install -g @xai/grok-build"),
			codingAgent("opencode", "npm install -g opencode"),
			{ID: "neuroforge-daemon", Category: CatDaemon, InstallHint: "started as a user service"},
		}
		return ProfileSpec{Profile: p, Tools: tools}
	case ProfileAndroid:
		base := ProfileSpecFor(ProfileStandard)
		base.Profile = p
		base.Tools = append(base.Tools,
			runtimeTool("java", "OpenJDK 17 (sdk install java 17.0.10-tem)"),
			ToolRequest{ID: "android-sdk", Category: CatMobile, NeedsSudo: false,
				ShellProfileChange: "export ANDROID_HOME=\"$HOME/Library/Android/sdk\"",
				InstallHint:        "Android command-line tools + platform-tools"},
		)
		return base
	case ProfileWeb:
		base := ProfileSpecFor(ProfileStandard)
		base.Profile = p
		base.Tools = append(base.Tools,
			runtimeTool("node", "Node.js LTS (brew install node)"),
		)
		return base
	case ProfileFull:
		base := ProfileSpecFor(ProfileAndroid)
		base.Profile = p
		base.Tools = append(base.Tools,
			runtimeTool("node", "Node.js LTS"),
			imageProvider("gpt-image", "OpenAI API key (official `codex login`)"),
			imageProvider("nano-banana", "Gemini API key (official `gemini login`)"),
			ToolRequest{ID: "docker", Category: CatContainer, NeedsSudo: true, InstallHint: "Docker Desktop / podman"},
		)
		return base
	default:
		// CUSTOM: empty; the user selects tools. The plan merges the selection.
		return ProfileSpec{Profile: p}
	}
}

func mustHave(id string, cat ToolCategory, hint string) ToolRequest {
	return ToolRequest{ID: id, Category: cat, InstallHint: hint}
}

func codingAgent(id, hint string) ToolRequest {
	return ToolRequest{ID: id, Category: CatCodingAgent, InstallHint: hint}
}

func runtimeTool(id, hint string) ToolRequest {
	return ToolRequest{ID: id, Category: CatRuntime, InstallHint: hint}
}

func imageProvider(id, hint string) ToolRequest {
	return ToolRequest{ID: id, Category: CatImageProvider, InstallHint: hint}
}

// SortedTools returns the profile's tool requests in stable id order.
func (s ProfileSpec) SortedTools() []ToolRequest {
	out := make([]ToolRequest, len(s.Tools))
	copy(out, s.Tools)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
