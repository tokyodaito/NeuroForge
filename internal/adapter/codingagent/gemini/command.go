package gemini

import (
	"strings"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// outputFormatJSON is the safe, structured output format the adapter requests.
// It produces a single final JSON response document (parsed by parseStream's
// document mode). The `stream-json` format is NOT used: translating it to
// incremental events is explicitly not implemented (§36.25).
const outputFormatJSON = "json"

// runSpec is the fully-resolved, deterministic invocation derived from an
// [protocol.AgentRunRequest]. It is argv-only (never a shell string) and never
// enables unsafe modes: YOLO / auto-approve-all / unrestricted file access are
// never added by the adapter (spec §29, task constraint).
type runSpec struct {
	// binary is the resolved Gemini CLI path.
	binary string
	// argv is the complete argument vector (excluding argv[0]=binary).
	argv []string
	// promptFile, when non-empty, indicates the prompt should be piped to the
	// child's stdin (the file is opened at launch). When empty, the prompt is
	// passed inline via -p.
	promptFile string
}

// buildRunSpec derives the deterministic headless argv from a run request.
//
// Command shape (safe default profile):
//
//	<binary> -p "<prompt>" -o json [-m <model>] [ExtraArgs...]
//
// When [protocol.AgentRunRequest.PromptFile] is set instead of an inline
// prompt, the prompt file is piped to stdin (the -p flag is omitted) so that
// arbitrarily large context packs never overflow the OS argv limit and never
// touch a shell.
//
// Model selection (-m) is added only when req.Model is non-empty (rule §36.8:
// the model is provider-supplied; the adapter never hard-codes one). TurnLimit
// is intentionally not mapped: the Gemini CLI has no stable headless turn-limit
// flag, so it is not implemented (§36.25) rather than approximated.
func buildRunSpec(binary string, req protocol.AgentRunRequest, extra []string) runSpec {
	argv := make([]string, 0, 8+len(extra))

	switch {
	case strings.TrimSpace(req.Prompt) != "":
		argv = append(argv, "-p", req.Prompt)
	case req.PromptFile != "":
		// Prompt arrives via stdin; -p is omitted. See [runSpec.promptFile].
	default:
		// No prompt supplied: pass an empty one so the CLI still runs headless.
		argv = append(argv, "-p", "")
	}

	argv = append(argv, "-o", outputFormatJSON)

	if req.Model != "" {
		argv = append(argv, "-m", req.Model)
	}

	argv = append(argv, extra...)

	spec := runSpec{binary: binary, argv: argv}
	if req.PromptFile != "" && strings.TrimSpace(req.Prompt) == "" {
		spec.promptFile = req.PromptFile
	}
	return spec
}

// argv0 returns the full argv vector including the binary as element 0, for
// spawning.
func (s runSpec) argv0() []string {
	return append([]string{s.binary}, s.argv...)
}
