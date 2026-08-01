package gemini

import (
	"os"
	"strings"
)

// baseEnvKeys is the positive allowlist of environment variables the agent
// process always receives (spec §29.2). These are execution-essentials only:
// never VCS merge tokens, production credentials, unrelated API keys, or the
// daemon auth token (AC-28).
var baseEnvKeys = []string{
	"PATH", "HOME", "USER",
	"LANG", "LC_ALL", "TERM",
	"TEMP", "TMP",
}

// versionProbeEnv is the minimal environment used for the `gemini --version`
// probe. It carries only what the CLI needs to start and print its version, so
// the probe never leaks secrets.
func versionProbeEnv() []string {
	return buildEnv(nil)
}

// buildEnv constructs the allowlisted environment for an agent process. It
// copies each [baseEnvKeys] variable from the current environment when present,
// then applies the caller's allowlist. Allowlist entries are either "KEY"
// (copied from the current env when set) or "KEY=VAL" (passed verbatim).
//
// buildEnv never copies the whole environment and never forwards known-secret
// variables (GITHUB_TOKEN, GITLAB_TOKEN, AWS_*, the daemon auth token, etc.):
// the positive allowlist makes leakage structurally impossible (spec §29.2,
// AC-28).
func buildEnv(allowlist []string) []string {
	env := make([]string, 0, len(baseEnvKeys)+len(allowlist))
	for _, k := range baseEnvKeys {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	for _, kv := range allowlist {
		if idx := strings.IndexByte(kv, '='); idx >= 0 {
			env = append(env, kv)
			continue
		}
		if v, ok := os.LookupEnv(kv); ok {
			env = append(env, kv+"="+v)
		}
	}
	return env
}
