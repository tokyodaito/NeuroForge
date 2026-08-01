package claude

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// baselineEnvKeys is the static environment allowlist every agent process
// receives (spec §29.2). It is the minimal set required for a CLI process to
// function (PATH for discovery, HOME for config, locale/terminal settings).
// It never includes the daemon auth token, VCS/merge tokens, or any provider
// credential — those reach the agent only via req.AllowlistEnv, which the
// supervisor constructs.
var baselineEnvKeys = []string{
	"PATH", "HOME", "USER", "LANG", "LC_ALL", "TERM",
}

// forbiddenEnvTokens are secret-bearing substrings that are dropped from
// req.AllowlistEnv even if the caller accidentally allowlists them (defense in
// depth for §29.2 / AC-28). Provider credentials (e.g. API key / OAuth token)
// are intentionally NOT in this list: the supervisor allowlists those on
// purpose and the agent needs them to authenticate.
var forbiddenEnvTokens = []string{
	"forge_daemon_token", "daemon_token", "forge_auth_token",
	"merge_token", "vcs_token",
	"github_token", "gitlab_token", "bitbucket_token", "azure_devops_token",
	"deploy_token",
}

func isForbiddenEnvKey(k string) bool {
	low := strings.ToLower(k)
	for _, t := range forbiddenEnvTokens {
		if strings.Contains(low, t) {
			return true
		}
	}
	return false
}

// probeEnv returns the baseline allowlist environment used by short-lived
// diagnostic probes (detect/version/health). It never includes allowlist
// secrets.
func probeEnv() []string { return buildEnv(nil) }

// buildEnv constructs the child-process environment from the baseline keys plus
// an optional caller allowlist. Entries are "KEY=VAL". Baseline keys are
// trusted (copied from the current env when present); allowlist entries are
// validated against the forbidden-token list. Duplicate keys (case-insensitive)
// are collapsed to the first occurrence.
func buildEnv(allowlist []string) []string {
	env := make([]string, 0, len(baselineEnvKeys)+len(allowlist))
	seen := map[string]bool{}
	add := func(k, v string, trusted bool) {
		key := strings.ToUpper(k)
		if seen[key] {
			return
		}
		if !trusted && isForbiddenEnvKey(k) {
			return
		}
		seen[key] = true
		env = append(env, k+"="+v)
	}
	for _, k := range baselineEnvKeys {
		if v, ok := os.LookupEnv(k); ok {
			add(k, v, true)
		}
	}
	for _, kv := range allowlist {
		key, val, hasEq := splitEnvEntry(kv)
		if !hasEq {
			v, ok := os.LookupEnv(key)
			if !ok {
				continue
			}
			add(key, v, false)
		} else {
			add(key, val, false)
		}
	}
	return env
}

func splitEnvEntry(kv string) (key, val string, hasEq bool) {
	if i := strings.IndexByte(kv, '='); i >= 0 {
		return kv[:i], kv[i+1:], true
	}
	return kv, "", false
}

// buildArgv constructs the deterministic headless argv for a Claude Code run.
// It is pure with respect to (opts, req, isResume): the prompt text never
// appears in argv (it is delivered via stdin by default), so argv is stable
// regardless of prompt size. The order is canonical and version-independent.
//
// The shape (see docs/adapters/claude.md):
//
//	<bin> -p --output-format stream-json --verbose
//	       [--bare] [--permission-mode <mode>]
//	       [--model <m>] [--max-turns <n>] [--effort <e>]
//	       [--add-dir <d> ...]
//	       [--resume <session>]
//	       [...ExtraArgs]
//
// `--dangerously-skip-permissions` / `--permission-mode bypassPermissions` are
// never emitted (see [validatePermissionMode] / [validateExtraArgs]).
func (a *Adapter) buildArgv(bin string, req protocol.AgentRunRequest, isResume bool) []string {
	argv := []string{bin, "-p", "--output-format", "stream-json", "--verbose"}
	if a.opts.Bare {
		argv = append(argv, "--bare")
	}
	argv = append(argv, "--permission-mode", a.opts.PermissionMode)
	if m := strings.TrimSpace(req.Model); m != "" {
		argv = append(argv, "--model", m)
	}
	if req.TurnLimit > 0 {
		argv = append(argv, "--max-turns", strconv.Itoa(req.TurnLimit))
	}
	if e := strings.TrimSpace(a.opts.Effort); e != "" {
		argv = append(argv, "--effort", e)
	}
	for _, d := range a.opts.AdditionalDirs {
		if d = strings.TrimSpace(d); d != "" {
			argv = append(argv, "--add-dir", d)
		}
	}
	if isResume && strings.TrimSpace(req.SessionID) != "" {
		argv = append(argv, "--resume", req.SessionID)
	}
	if extra := a.opts.ExtraArgs; len(extra) > 0 {
		argv = append(argv, extra...)
	}
	return argv
}

// resolvePrompt returns the prompt text for a run: req.Prompt verbatim, else the
// contents of req.PromptFile. An empty prompt is permitted (the caller is
// responsible for providing one).
func (a *Adapter) resolvePrompt(req protocol.AgentRunRequest) (string, error) {
	if p := req.Prompt; strings.TrimSpace(p) != "" {
		return p, nil
	}
	if pf := strings.TrimSpace(req.PromptFile); pf != "" {
		b, err := os.ReadFile(pf)
		if err != nil {
			return "", fmt.Errorf("claude: read prompt file: %w", err)
		}
		return string(b), nil
	}
	return "", nil
}

// useStdin reports whether the prompt should be piped via stdin (true unless
// the caller configured PromptPositional).
func (a *Adapter) useStdin() bool { return a.opts.PromptStrategy != PromptPositional }
