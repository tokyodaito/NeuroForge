package codex

import (
	"errors"
	"strings"
)

// buildExecArgv constructs the exact headless argv for a "codex exec" run from
// the resolved binary path, the run request and the sandbox/approval flags. It is
// pure and deterministic so it can be unit-tested directly.
//
// The result is argv-only (no shell, no "/bin/sh"): every value becomes one
// exec argument, so spaces and Unicode in the workspace path or prompt need no
// quoting. The model selector is omitted when empty (Codex falls back to its
// configured default). The prompt is passed as the final positional argument.
func buildExecArgv(binary, model, prompt string, execArgs []string, isResume bool, resumeSession string) ([]string, error) {
	if strings.TrimSpace(binary) == "" {
		return nil, errors.New("codex: binary path is required")
	}
	args := execArgs
	if args == nil {
		args = DefaultExecArgs
	}

	argv := []string{binary, "exec"}
	argv = append(argv, args...)

	// Resume comes before the model selector/prompt. We pass the session id
	// Codex emitted at start time so the continuation re-attaches to the same
	// thread. Only added when resuming with a concrete session.
	if isResume && strings.TrimSpace(resumeSession) != "" {
		argv = append(argv, "--resume", resumeSession)
	}

	if strings.TrimSpace(model) != "" {
		argv = append(argv, "--model", model)
	}

	// The prompt is the final positional argument. It is omitted when absent so
	// the adapter faithfully translates the request rather than fabricating a
	// prompt (the supervisor always supplies one for real runs; an omitted
	// positional lets the conformance suite drive the adapter too).
	if strings.TrimSpace(prompt) != "" {
		argv = append(argv, prompt)
	}
	return argv, nil
}

// versionArgv builds the argv for a "codex --version" detection probe.
func versionArgv(binary string) []string { return []string{binary, "--version"} }
