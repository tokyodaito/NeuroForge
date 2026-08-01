package codex

import (
	"os"
	"strings"
)

// baseEnvNames is the positive allowlist of OS-essential environment variables
// passed to every Codex process (spec §29.2). It is deliberately minimal and
// never includes merge tokens, production credentials, unrelated API keys or the
// daemon auth token (AC-28).
var baseEnvNames = []string{
	"PATH",
	"HOME", "USER", "LOGNAME",
	"LANG", "LC_ALL", "LC_CTYPE",
	"TERM", "SHELL", "TZ",
	"TEMP", "TMP", "TMPDIR",
}

// forbiddenEnvPrefixes lists environment-variable name prefixes that must NEVER
// reach the Codex process, even if they appeared in [protocol.AgentRunRequest.AllowlistEnv]
// by mistake (spec §29.2, AC-28). This mirrors
// internal/supervisor.ForbiddenEnvPrefixes; it is duplicated here so the adapter
// does not depend on a core package (package-boundary rule in AGENTS.md).
var forbiddenEnvPrefixes = []string{
	"GITHUB_TOKEN", "GH_TOKEN", "GH_ENTERPRISE_TOKEN", "GITLAB_TOKEN",
	"GIT_TOKEN", "GIT_PASSWORD", "GIT_AUTHOR", "GIT_COMMITTER",
	"BITBUCKET", "AWS_SECRET", "AZURE_CLIENT_SECRET", "GOOGLE_APPLICATION",
	"NEUROFORGE_DAEMON", "NEUROFORGE_HOME", "FORGE_DAEMON_TOKEN", "FORGE_TOKEN",
	"DOCKER_PASSWORD", "KUBECONFIG", "SSH_AUTH_SOCK",
	"PG_PASSWORD", "POSTGRES_PASSWORD", "DATABASE_URL",
	"STRIPE", "TWILIO", "SENDGRID",
	// Provider keys the Codex CLI itself might otherwise pick up are NOT
	// forwarded by the adapter; the account's credentials are resolved by the
	// daemon's secret store, never via the agent environment.
	"OPENAI_API_KEY", "ANTHROPIC_API_KEY",
}

// buildAgentEnv constructs the allowlisted environment for a Codex process from
// the daemon's own environment plus the per-run [protocol.AgentRunRequest.AllowlistEnv].
//
// Only variables on baseEnvNames or the caller's allowlist survive, and any
// variable matching forbiddenEnvPrefixes is rejected as defence-in-depth. The
// result is deterministic (base names in declared order, then allowlist in
// order). Env keys are compared case-insensitively for de-duplication.
func buildAgentEnv(allowlist []string) []string {
	seen := make(map[string]bool, len(baseEnvNames)+len(allowlist))
	out := make([]string, 0, len(baseEnvNames)+len(allowlist))

	add := func(name string) {
		if name == "" {
			return
		}
		key := strings.ToUpper(name)
		if seen[key] {
			return
		}
		if isForbiddenEnv(name) {
			return
		}
		if v, ok := os.LookupEnv(name); ok {
			out = append(out, name+"="+v)
			seen[key] = true
		}
	}

	for _, n := range baseEnvNames {
		add(n)
	}
	for _, kv := range allowlist {
		// allowlist entries are "KEY" (copied from the current env) or "KEY=VAL".
		if idx := strings.IndexByte(kv, '='); idx >= 0 {
			name := kv[:idx]
			if isForbiddenEnv(name) || seen[strings.ToUpper(name)] {
				continue
			}
			out = append(out, kv)
			seen[strings.ToUpper(name)] = true
			continue
		}
		add(kv)
	}
	return out
}

// isForbiddenEnv reports whether name matches any forbidden prefix
// (case-insensitive).
func isForbiddenEnv(name string) bool {
	upper := strings.ToUpper(name)
	for _, prefix := range forbiddenEnvPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}
