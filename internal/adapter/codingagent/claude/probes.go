package claude

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// defaultModels returns the documented Claude Code `--model` aliases as the
// provider-supplied catalogue (rule §36.8: model names are provider-supplied,
// never hard-coded in the core; these aliases are the engine's own documented
// selectors, not version-pinned model ids). Callers may override via
// [Options.Models].
func defaultModels() []protocol.ModelDescriptor {
	aliases := []string{"sonnet", "opus", "haiku"}
	out := make([]protocol.ModelDescriptor, 0, len(aliases))
	for _, a := range aliases {
		out = append(out, protocol.ModelDescriptor{
			ID:             EngineID + "/" + a,
			Engine:         EngineID,
			Kind:           protocol.ModelKindCoding,
			ContextWindow:  200000,
			MaxOutput:      8192,
			SupportsImages: true,
			CachedUsage:    true,
		})
	}
	return out
}

// ListModels implements codingagent.Adapter. It returns the configured
// provider-supplied model catalogue (Options.Models / defaults). No network or
// paid call is made; the CLI does not expose a stable `claude models` command.
func (a *Adapter) ListModels(context.Context, protocol.Account) ([]protocol.ModelDescriptor, error) {
	out := make([]protocol.ModelDescriptor, len(a.opts.Models))
	copy(out, a.opts.Models)
	return out, nil
}

// InspectQuota implements codingagent.Adapter. Claude Code does not expose a
// reliable, authoritative remaining-quota figure through the CLI; subscription
// quota is distinct from API rate-limit and must not be reported as more
// precise than warranted (rule §36.10). The adapter therefore reports UNKNOWN.
// The supervisor infers quota state from observed failure signals (§20.1
// INFERRED) via [Adapter.ClassifyFailure].
func (a *Adapter) InspectQuota(context.Context, protocol.Account) protocol.QuotaSnapshot {
	return protocol.QuotaSnapshot{
		Confidence: protocol.QuotaConfUnknown,
		State:      protocol.QuotaStateUnknown,
		Reason:     "claude code exposes no authoritative remaining-quota figure; subscription quota is distinct from API rate-limit",
	}
}

// authStatusJSON is the subset of `claude auth status` JSON the adapter reads.
type authStatusJSON struct {
	LoggedIn bool   `json:"loggedIn"`
	Account  string `json:"account,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Health implements codingagent.Adapter. It runs `claude auth status` (which
// exits 0 when logged in and 1 otherwise, per the CLI reference) and maps it:
//
//   - installed + logged in      → ok
//   - installed + not logged in  → degraded (engine present, account needs auth)
//   - auth subcommand missing    → unknown (older CLI)
//   - not installed / launch err → down
//
// Health never performs paid work and never hangs beyond ProbeTimeout.
func (a *Adapter) Health(ctx context.Context, account protocol.Account) protocol.HealthResult {
	bin, err := a.binary()
	if err != nil {
		return protocol.HealthResult{Status: protocol.HealthDown, Detail: "claude not installed: " + err.Error()}
	}
	start := a.opts.Now()
	stdout, stderr, exitCode, perr := a.runProbe(ctx, bin, []string{"auth", "status"})
	latency := a.opts.Now().Sub(start)
	if perr != nil {
		// Could not launch the probe; engine is unusable on this host right now.
		return protocol.HealthResult{Status: protocol.HealthDown, Detail: "auth probe failed: " + perr.Error(), Latency: latency}
	}
	// Attempt to parse JSON for richer detail (non-fatal when output is text).
	as := parseAuthStatus(stdout, stderr)
	detail := authDetail(as, stderr)
	switch exitCode {
	case 0:
		return protocol.HealthResult{Status: protocol.HealthOK, Detail: detail, Latency: latency}
	case 1:
		// Exited 1: either "not logged in" (degraded) or "unknown subcommand".
		if authSubcommandMissing(stderr) {
			return protocol.HealthResult{Status: protocol.HealthUnknown, Detail: "claude auth status unavailable on this CLI version" + detailSuffix(stderr), Latency: latency}
		}
		return protocol.HealthResult{Status: protocol.HealthDegraded, Detail: "not logged in" + detailSuffix(stderr), Latency: latency}
	default:
		return protocol.HealthResult{Status: protocol.HealthUnknown, Detail: "auth status exited " + itoa(exitCode) + detailSuffix(stderr), Latency: latency}
	}
}

func parseAuthStatus(stdout, stderr []byte) authStatusJSON {
	var as authStatusJSON
	if len(stdout) > 0 {
		if err := json.Unmarshal(stdout, &as); err == nil {
			return as
		}
	}
	return authStatusJSON{}
}

func authDetail(as authStatusJSON, stderr []byte) string {
	parts := []string{}
	if as.Account != "" {
		parts = append(parts, "account="+as.Account)
	}
	if as.Mode != "" {
		parts = append(parts, "mode="+as.Mode)
	}
	if as.Error != "" {
		parts = append(parts, "err="+as.Error)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func detailSuffix(stderr []byte) string {
	s := strings.TrimSpace(string(stderr))
	if s == "" {
		return ""
	}
	return ": " + firstLine(s)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// authSubcommandMissing detects an older CLI that does not implement `auth
// status` (it typically prints a usage/error mentioning the unknown command).
func authSubcommandMissing(stderr []byte) bool {
	low := strings.ToLower(string(stderr))
	return strings.Contains(low, "unknown command") ||
		strings.Contains(low, "unknown subcommand") ||
		strings.Contains(low, "not a recognized") ||
		strings.Contains(low, "invalid command")
}

// itoa is a dependency-free int → string used in diagnostics.
func itoa(n int) string { return strconv.Itoa(n) }
