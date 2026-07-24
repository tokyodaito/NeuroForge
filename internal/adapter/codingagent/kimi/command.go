package kimi

import (
	"neuroforge/internal/adapter/codingagent/protocol"
)

// buildArgv constructs the exact headless argv for a Kimi run from the request.
// It is a pure function of its inputs: no I/O, no shell, no globbing — the
// returned slice is passed straight to exec (argv-only), so spaces, quotes and
// Unicode in the workspace/prompt need no shell escaping (spec §17, §29).
//
// The headless entry point is `kimi -p <prompt>` with `--output stream-json`
// for incremental structured output. Model selection, turn limits and session
// resume are added only when the detected version/support probe indicates the
// flag is honoured, so an unsupported flag is never sent to an older engine.
func buildArgv(req runSpec, pf probedFlags, profile versionProfile) []string {
	argv := []string{}

	// Non-interactive prompt mode is the baseline entry point.
	prompt := req.prompt
	if prompt == "" {
		// Kimi's -p requires an argument; use a neutral placeholder when the
		// caller supplied neither an inline prompt nor a readable prompt file.
		prompt = " "
	}
	argv = append(argv, "-p", prompt)

	// Streaming structured output, when supported.
	if wantStream(req, pf, profile) {
		argv = append(argv, "--output", "stream-json")
	}

	// Model selection (engine != model, spec §12.1).
	if req.model != "" && supportFlag(pf.model, profile.flagModel) {
		argv = append(argv, "--model", req.model)
	}

	// Turn limit, when requested and supported.
	if req.turnLimit > 0 && supportFlag(pf.maxTurns, profile.flagMaxTurns) {
		argv = append(argv, "--max-turns", itoa(req.turnLimit))
	}

	// Session resume via --continue, only when genuinely supported and the run
	// is a resume with a session id (spec §21).
	if req.isResume && req.sessionID != "" && supportFlag(pf.continued, profile.flagContinue) {
		argv = append(argv, "--continue", req.sessionID)
	}

	argv = append(argv, req.extraArgs...)
	return argv
}

// runSpec is the request reduced to the fields the command builder needs. Start
// resolves prompt/promptFile into a single prompt string before building.
type runSpec struct {
	prompt     string
	model      string
	sessionID  string
	turnLimit  int
	isResume   bool
	extraArgs  []string
	streamHint bool // caller forces streaming regardless of version (Options.ForceStreaming)
}

// wantStream reports whether the run should request stream-json output.
func wantStream(req runSpec, pf probedFlags, profile versionProfile) bool {
	if req.streamHint {
		return true
	}
	// Prefer the probed flag; fall back to the version-gated default.
	if pf.streamJSON {
		return true
	}
	return profile.flagStreamJSON
}

// supportFlag reconciles a probed flag with the version-gated default. A probed
// result is authoritative when available; otherwise the version gate decides.
func supportFlag(probed, versionGated bool) bool {
	// If we probed help, trust it; if not, trust the version gate. Both default
	// to false on older/unknown engines, so unsupported flags are never sent.
	return probed || versionGated
}

// itoa formats a non-negative int without importing strconv in this hot path.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// resolvePrompt returns the prompt string to pass to `-p`. An inline prompt
// wins; otherwise the prompt file is read. An empty result is valid (the
// builder substitutes a placeholder).
func resolvePrompt(req protocol.AgentRunRequest, readPromptFile func(string) (string, error)) (string, error) {
	if req.Prompt != "" {
		return req.Prompt, nil
	}
	if req.PromptFile != "" && readPromptFile != nil {
		b, err := readPromptFile(req.PromptFile)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	return "", nil
}
