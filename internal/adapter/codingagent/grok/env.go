package grok

import (
	"os"
	"runtime"
	"strings"
)

// allowlistKeys are the always-permitted, non-secret environment variables
// propagated to the Grok child process (spec §29.2, AC-28). They cover PATH
// resolution, locale, terminal and the Windows system directories/temp that the
// CLI needs to run. VCS merge tokens, the daemon auth token, and unrelated API
// keys are NEVER in this set and are therefore never inherited.
var allowlistKeys = []string{
	"PATH",
	"HOME",
	"USERPROFILE",
	"USER",
	"LANG",
	"LC_ALL",
	"TERM",
	"SystemRoot",
	"TEMP",
	"TMP",
}

// buildEnv constructs the child process environment (spec §29.2): only the
// allowlisted keys from the current process, plus the caller's per-request
// allowlist (AllowlistEnv), plus the adapter's test-only ExtraEnv.
//
// AllowlistEnv entries are either "KEY" (copied from the current environment if
// present) or "KEY=VAL" (passed verbatim). Matching is case-insensitive on
// Windows (where env keys are case-insensitive); the canonical requested casing
// is preserved on the child env to avoid surprising the CLI.
func buildEnv(reqAllowlist []string, extraEnv []string) []string {
	env := make([]string, 0, len(allowlistKeys)+len(reqAllowlist)+len(extraEnv))
	seen := newKeySet()

	for _, k := range allowlistKeys {
		if addFromCurrent(&env, seen, k) {
			continue
		}
	}
	for _, kv := range reqAllowlist {
		addAllowlistEntry(&env, seen, kv)
	}
	// ExtraEnv is adapter-level (test stubs); appended last so it cannot
	// smuggle secrets past the per-request allowlist in production wiring (the
	// daemon never populates Options.ExtraEnv — see options.go).
	for _, kv := range extraEnv {
		env = append(env, kv)
	}
	return env
}

// addFromCurrent appends KEY=<current value> if the current environment defines
// KEY (case-insensitively on Windows). Returns true if a value was added.
func addFromCurrent(env *[]string, seen *keySet, key string) bool {
	if idx := lookupEnvIndex(key); idx >= 0 {
		*env = append(*env, os.Environ()[idx])
		seen.add(key)
		return true
	}
	return false
}

// addAllowlistEntry honours "KEY" (copy from current) and "KEY=VAL" (verbatim).
func addAllowlistEntry(env *[]string, seen *keySet, kv string) {
	if idx := strings.IndexByte(kv, '='); idx >= 0 {
		key := kv[:idx]
		if seen.has(key) {
			return
		}
		*env = append(*env, kv)
		seen.add(key)
		return
	}
	// Bare KEY: copy from the current environment if present.
	addFromCurrent(env, seen, kv)
}

// lookupEnvIndex returns the index into os.Environ() of KEY (case-insensitive on
// Windows), or -1.
func lookupEnvIndex(key string) int {
	all := os.Environ()
	for i, kv := range all {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		k := kv[:eq]
		if envKeysEqual(k, key) {
			return i
		}
	}
	return -1
}

func envKeysEqual(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// keySet is a case-aware (Windows: case-insensitive) set of env keys already
// added, so the first definition wins and duplicates never appear.
type keySet struct {
	m map[string]struct{}
}

func newKeySet() *keySet { return &keySet{m: map[string]struct{}{}} }

func (s *keySet) add(key string) {
	if s.m == nil {
		s.m = map[string]struct{}{}
	}
	s.m[normalizeKey(key)] = struct{}{}
}

func (s *keySet) has(key string) bool {
	if s.m == nil {
		return false
	}
	_, ok := s.m[normalizeKey(key)]
	return ok
}

func normalizeKey(key string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(key)
	}
	return key
}
