package supervisor

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ForbiddenEnvPrefixes lists environment variable name prefixes that must NEVER
// be passed to an agent process (spec §29.2, AC-28). These include VCS merge
// tokens, production credentials, unrelated API keys, and the daemon auth token.
var ForbiddenEnvPrefixes = []string{
	"GITHUB_TOKEN", "GH_TOKEN", "GH_ENTERPRISE_TOKEN", "GITLAB_TOKEN",
	"GIT_TOKEN", "GIT_PASSWORD", "GIT_AUTHOR", "GIT_COMMITTER",
	"BITBUCKET", "AWS_SECRET", "AZURE_CLIENT_SECRET", "GOOGLE_APPLICATION",
	"NEUROFORGE_DAEMON", "NEUROFORGE_HOME",
	"DOCKER_PASSWORD", "KUBECONFIG", "SSH_AUTH_SOCK",
	"PG_PASSWORD", "POSTGRES_PASSWORD", "DATABASE_URL",
	"STRIPE", "TWILIO", "SENDGRID",
}

// AllowedEnvNames is the allowlist of environment variables that ARE passed to
// agent processes. It is deliberately minimal: PATH, HOME, USER, LANG/LC_*,
// TERM, SHELL, TMPDIR, and a few essentials. Everything else is stripped (spec
// §29.2: use an allowlist, never pass the whole environment).
var AllowedEnvNames = []string{
	"PATH", "HOME", "USER", "LOGNAME",
	"LANG", "LC_ALL", "LC_CTYPE",
	"TERM", "SHELL", "TMPDIR", "TEMP", "TMP",
	"TZ",
	// Git identity defaults so checkpoint commits work without the user's
	// global config (the workspace manager overrides these per-commit anyway).
	"GIT_CONFIG_NOSYSTEM",
}

// EnvAllowlist builds a safe, minimal environment for an agent process from the
// daemon's full environment. It applies a positive allowlist: only variables on
// [AllowedEnvNames] survive, and any variable matching a [ForbiddenEnvPrefixes]
// entry is explicitly rejected even if it appears on the allowlist by mistake.
//
// The result never contains merge credentials, production secrets, unrelated API
// keys, or the daemon auth token (spec §29.2, AC-28).
func EnvAllowlist(fullEnv []string) []string {
	allowedSet := make(map[string]bool, len(AllowedEnvNames))
	for _, name := range AllowedEnvNames {
		allowedSet[name] = true
	}

	out := make([]string, 0, len(AllowedEnvNames)+2)
	seen := make(map[string]bool)

	for _, kv := range fullEnv {
		name, _, _ := strings.Cut(kv, "=")
		if name == "" || seen[name] {
			continue
		}
		if !allowedSet[name] {
			continue // not on the allowlist -> stripped
		}
		if isForbidden(name) {
			continue // defense-in-depth: never pass a forbidden var
		}
		out = append(out, kv)
		seen[name] = true
	}

	// Sort for determinism (makes tests stable and env diffs readable).
	sort.Strings(out)
	return out
}

// isForbidden reports whether name matches any forbidden prefix.
func isForbidden(name string) bool {
	upper := strings.ToUpper(name)
	for _, prefix := range ForbiddenEnvPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

// AssertEnvSafe returns an error if env contains any variable matching the
// forbidden prefixes. It is used by tests and the supervisor to verify the
// allowlist is correct before launching an agent.
func AssertEnvSafe(env []string) error {
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if isForbidden(name) {
			return fmt.Errorf("supervisor: forbidden env var %q would leak to agent (AC-28)", name)
		}
	}
	return nil
}

// ErrEnvLeak is returned when the environment allowlist check detects a leak.
var ErrEnvLeak = errors.New("supervisor: environment leak detected")
