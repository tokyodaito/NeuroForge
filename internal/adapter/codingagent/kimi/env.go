package kimi

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// baseEnvKeys returns the OS-essential environment keys the adapter is allowed
// to copy from the host environment. This is the fixed, minimal set (spec
// §29.2): nothing credential-shaped is ever in it. Windows-specific keys
// (SystemRoot/USERPROFILE/TEMP/TMP) are included because the OS needs them to
// function; on other platforms they are simply absent and skipped.
func baseEnvKeys() []string {
	keys := []string{"PATH", "HOME", "USER", "LANG", "LC_ALL", "TERM", "USERPROFILE", "SystemRoot", "TEMP", "TMP"}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// buildRunEnv constructs the allowlisted environment for an agent process (spec
// §29.2, AC-28). It contains ONLY:
//
//   - the OS-essential base keys (PATH/HOME/TERM/...), copied from the host;
//   - the per-run isolated home (homeEnvName=homeDir), relocating Kimi's profile;
//   - non-interactive hints (NO_COLOR=1) so the stream is free of colour codes;
//   - the caller's explicit [protocol.AgentRunRequest.AllowlistEnv] entries
//     (each either "KEY" copied from the host, or "KEY=VAL" verbatim);
//   - the adapter's [Options.ExtraEnv] entries (KEY=VAL verbatim).
//
// It NEVER copies the entire host environment, and therefore never forwards VCS
// merge tokens, production credentials, unrelated API keys, or the daemon auth
// token. Case-insensitive duplicate keys: the first occurrence wins (later
// entries are ignored) so an allowlist entry cannot shadow the isolated home.
func buildRunEnv(homeEnvName, homeDir string, allowlist, extra []string) []string {
	seen := make(map[string]struct{}, 16)
	var env []string

	add := func(kv string) {
		key, _, _ := splitEnvEntry(kv)
		if key == "" {
			// Allow NO_COLOR=1 etc. (always has '='), but skip malformed entries.
			if strings.IndexByte(kv, '=') < 0 {
				return
			}
			key = kv[:strings.IndexByte(kv, '=')]
		}
		if _, dup := seen[strings.ToUpper(key)]; dup {
			return
		}
		seen[strings.ToUpper(key)] = struct{}{}
		env = append(env, kv)
	}

	for _, kv := range baseEnvKeys() {
		add(kv)
	}
	if homeEnvName != "" && homeDir != "" {
		add(homeEnvName + "=" + homeDir)
	}
	add("NO_COLOR=1")

	for _, kv := range allowlist {
		if idx := strings.IndexByte(kv, '='); idx >= 0 {
			// Literal KEY=VAL.
			add(kv)
			continue
		}
		// Bare KEY: copy from the host environment if present.
		if v, ok := os.LookupEnv(kv); ok {
			add(kv + "=" + v)
		}
	}
	for _, kv := range extra {
		add(kv)
	}
	return env
}

// splitEnvEntry splits a "KEY=VAL" entry into its key and value.
func splitEnvEntry(kv string) (key, val string, ok bool) {
	idx := strings.IndexByte(kv, '=')
	if idx < 0 {
		return "", "", false
	}
	return kv[:idx], kv[idx+1:], true
}

// isolatedHomeDir returns the per-run directory used to relocate Kimi's
// config/home, creating it if needed. It is rooted in the run workspace (so a
// run never mutates the user's global profile and archives stay inside the
// worktree) and falls back to a process temp dir when no workspace is provided.
// The directory is namespaced (.neuroforge-kimi) to avoid colliding with the
// engine's own state.
func isolatedHomeDir(workspace string) (string, error) {
	base := workspace
	if base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, ".neuroforge-kimi")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// secretPatterns are credential-shaped substrings scrubbed from captured stderr
// before it is surfaced in events or logs. Redaction is best-effort: the
// adapter already avoids forwarding secrets, so this is a defensive backstop.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(sk-)[A-Za-z0-9._-]{6,}`),
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._\-+/=]+`),
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*)\S+`),
	regexp.MustCompile(`(?i)((?:api[_-]?key|token|password|passwd|secret|access[_-]?token)\s*[:=]\s*)\S+`),
}

// redact masks credential-shaped substrings in s.
func redact(s string) string {
	for _, re := range secretPatterns {
		s = re.ReplaceAllString(s, "${1}REDACTED")
	}
	return s
}
