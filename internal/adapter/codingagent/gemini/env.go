package gemini

import (
	"os"
	"runtime"
	"strings"
)

// baseEnvKeys is the positive allowlist of environment variables the agent
// process always receives (spec §29.2). These are execution-essentials only:
// never VCS merge tokens, production credentials, unrelated API keys, or the
// daemon auth token (AC-28).
//
// The set is Windows-aware: USERPROFILE, SystemRoot, TEMP and TMP are required
// for Node.js (the Gemini CLI runtime) to function on Windows. They are
// harmlessly absent on Unix.
var baseEnvKeys = []string{
	"PATH", "HOME", "USERPROFILE", "USER",
	"LANG", "LC_ALL", "TERM",
	"SystemRoot", "TEMP", "TMP",
}

// versionProbeEnv is the minimal environment used for the `gemini --version`
// probe. It carries only what the CLI needs to start and print its version, so
// the probe never leaks secrets and works on both Windows and Unix.
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
		if v, ok := lookupEnvCI(k); ok {
			env = append(env, k+"="+v)
		}
	}
	for _, kv := range allowlist {
		if idx := strings.IndexByte(kv, '='); idx >= 0 {
			env = append(env, kv)
			continue
		}
		if v, ok := lookupEnvCI(kv); ok {
			env = append(env, kv+"="+v)
		}
	}
	return env
}

// lookupEnvCI looks up an environment variable case-insensitively on Windows
// (where env keys are case-insensitive) and case-sensitively elsewhere. This
// matches how the OS resolves keys for a child process.
func lookupEnvCI(key string) (string, bool) {
	if v, ok := os.LookupEnv(key); ok {
		return v, true
	}
	// Windows: env keys are case-insensitive; fall back to a case-insensitive
	// scan so "PATH" matches "Path". os.LookupEnv already honours this on
	// Windows, but we scan explicitly to be robust to cross-tool edge cases
	// (e.g. a key set via a shim with different casing).
	if !caseInsensitiveEnv {
		return "", false
	}
	lower := strings.ToLower(key)
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			if strings.ToLower(kv[:i]) == lower {
				return kv[i+1:], true
			}
		}
	}
	return "", false
}

// caseInsensitiveEnv reports whether the platform treats environment keys
// case-insensitively (Windows). Inlined as a const so the branch folds away on
// Unix without a runtime check.
const caseInsensitiveEnv = (runtime.GOOS == "windows")
