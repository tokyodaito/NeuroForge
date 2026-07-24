package grok

import (
	"strconv"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// buildArgv constructs the deterministic headless argv for a Grok run from an
// [protocol.AgentRunRequest] and the resolved capabilities. It is pure: no
// shell, no /bin/sh, no quoting (argv-only) so spaces/Unicode in the workspace
// or prompt are passed verbatim. The same request + capabilities always yield
// the same argv.
//
// The certain core is always emitted:
//
//	<binary> --no-auto-update -p --output-format streaming-json
//
// followed by version/capability-gated options (--model, --resume, turn limit)
// and finally the prompt positional (PromptFile path or inline Prompt).
func buildArgv(binary string, req protocol.AgentRunRequest, caps protocol.AgentCapabilities, enableTurnLimit bool) []string {
	argv := []string{
		binary,
		"--no-auto-update", // rule §36.19: never update a provider CLI mid-run
		"-p",               // headless / non-interactive print mode
		"--output-format", "streaming-json",
	}

	if caps.ModelSelection && req.Model != "" {
		argv = append(argv, "--model", req.Model)
	}
	if caps.SessionResume && req.SessionID != "" {
		argv = append(argv, "--resume", req.SessionID)
	}
	if enableTurnLimit && req.TurnLimit > 0 {
		// Flag name ASSUMED (rule §36.25); only emitted when explicitly enabled.
		argv = append(argv, "--max-turns", strconv.Itoa(req.TurnLimit))
	}

	// Prompt: PromptFile wins (compiled task prompt on disk, spec §22.3),
	// otherwise inline Prompt. Both are passed positionally; Grok's headless
	// mode reads the task from the first positional argument.
	switch {
	case req.PromptFile != "":
		argv = append(argv, req.PromptFile)
	case req.Prompt != "":
		argv = append(argv, req.Prompt)
	}
	return argv
}
