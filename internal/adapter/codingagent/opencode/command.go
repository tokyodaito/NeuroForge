package opencode

import (
	"os"
	"strings"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// buildArgv constructs the exact, deterministic headless argv for an OpenCode
// run from a [protocol.AgentRunRequest] (spec §12.2 Start). It emits argv ONLY
// — never a shell string — so spaces and Unicode in paths are handled natively
// by the OS process spawn. Only documented OpenCode `run` flags are used (see
// docs/adapters/opencode.md).
//
// `--share` is NEVER emitted: NeuroForge-managed runs are never shared.
func (a *Adapter) buildArgv(req protocol.AgentRunRequest, isResume bool) []string {
	bin := a.opts.Binary
	if bin == "" {
		bin = binaryName
	}
	argv := []string{bin, "run", "--format", "json"}
	if dir := strings.TrimSpace(req.Workspace); dir != "" {
		argv = append(argv, "--dir", dir)
	}
	if m := strings.TrimSpace(req.Model); m != "" {
		argv = append(argv, "--model", m)
	}
	if ag := strings.TrimSpace(a.opts.Agent); ag != "" {
		argv = append(argv, "--agent", ag)
	}
	if isResume && strings.TrimSpace(req.SessionID) != "" {
		argv = append(argv, "--session", req.SessionID)
	}
	// Caller-supplied documented flags only. These MUST NOT include --share and
	// MUST NOT weaken NeuroForge policy; callers are responsible for content.
	if len(a.opts.ExtraArgs) > 0 {
		argv = append(argv, a.opts.ExtraArgs...)
	}
	// Prompt: prefer an inline prompt, fall back to a prompt file path. When
	// neither is set, no message argument is emitted (opencode reads none).
	switch {
	case strings.TrimSpace(req.Prompt) != "":
		argv = append(argv, req.Prompt)
	case strings.TrimSpace(req.PromptFile) != "":
		argv = append(argv, req.PromptFile)
	}
	return argv
}

// baseEnvKeys are the allowlisted environment variables always forwarded to the
// agent process when present (spec §29.2). They contain no secrets and are
// required for the engine, the OS and locale to function.
var baseEnvKeys = []string{
	"PATH", "HOME", "USER", "LANG", "LC_ALL", "TERM",
	"TEMP", "TMP",
}

// buildEnv constructs the allowlisted environment for the agent process (spec
// §29.2, AC-28): only the [baseEnvKeys] plus the caller's
// [protocol.AgentRunRequest.AllowlistEnv] are forwarded. The environment is
// built from scratch — never derived from os.Environ — so VCS merge tokens,
// production credentials, unrelated API keys and the daemon auth token can never
// leak into the agent process.
//
// Each AllowlistEnv entry is either "KEY" (copied from the current environment
// if set) or "KEY=VAL" (forwarded verbatim).
func buildEnv(allowlist []string) []string {
	env := make([]string, 0, len(baseEnvKeys)+len(allowlist))
	appendEnv := func(key string) {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	for _, k := range baseEnvKeys {
		appendEnv(k)
	}
	for _, kv := range allowlist {
		if idx := strings.IndexByte(kv, '='); idx >= 0 {
			key := kv[:idx]
			// Redact defensively: an allowlist value that looks like a known
			// secret key is still forwarded (the supervisor's allowlist is
			// trusted), but a forbidden credential key is dropped.
			if isForbiddenCredentialKey(key) {
				continue
			}
			env = append(env, kv)
			continue
		}
		if isForbiddenCredentialKey(kv) {
			continue
		}
		appendEnv(kv)
	}
	return env
}

// forbiddenCredentialKeys are substrings (lowercased) that mark an environment
// key as a secret that must NEVER be forwarded even if it appears in an
// allowlist (defense-in-depth against a misconfigured supervisor). Spec §29.2.
var forbiddenCredentialKeyFragments = []string{
	"forge_daemon_token", // NeuroForge daemon auth token
	"merge_token",        // VCS merge credential
	"github_token", "gitlab_token", "gittoken",
	"vcs_token",
	"aws_secret", "stripe_", "production_secret",
}

func isForbiddenCredentialKey(key string) bool {
	low := strings.ToLower(strings.TrimSpace(key))
	for _, bad := range forbiddenCredentialKeyFragments {
		if strings.Contains(low, bad) {
			return true
		}
	}
	return false
}
