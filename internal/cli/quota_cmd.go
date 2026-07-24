package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"neuroforge/internal/quota"
	"neuroforge/internal/router/fakes"
)

// runQuota implements `forge quota` — show provider quota per account (spec
// §20). Estimated quota is rendered distinctly from exact (rule §36.10, AC-18).
func (a *App) runQuota(args []string) int {
	fs := flag.NewFlagSet("quota", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}

	r := fakes.DefaultRouter()
	snaps := r.Catalog().Accounts()
	// Build a snapshot per catalog account from the quota manager.
	mgr := fakes.DefaultQuotaManager(r.Catalog())
	rows := make([]quota.Snapshot, 0, len(snaps))
	for _, id := range snaps {
		rows = append(rows, mgr.Snapshot(id))
	}

	if *jsonOut {
		payload := make([]quotaJSON, 0, len(rows))
		for _, s := range rows {
			payload = append(payload, quotaJSON{
				Engine: s.Account.Engine, Account: s.Account.Account,
				State: string(s.State), Confidence: string(s.Confidence),
				Remaining: quota.FormatRemaining(s), Limit: quota.FormatLimit(s),
			})
		}
		b, _ := json.MarshalIndent(payload, "", "  ")
		fmt.Fprintln(a.Out, string(b))
		return ExitOK
	}

	if len(rows) == 0 {
		fmt.Fprintln(a.Out, "No provider accounts configured.")
		return ExitOK
	}

	fmt.Fprintln(a.Out, boldPlain+"PROVIDER QUOTA"+reset)
	fmt.Fprintln(a.Out)
	fmt.Fprintf(a.Out, "%-12s %-14s %-12s %-16s %-12s %s\n", "ENGINE", "ACCOUNT", "STATE", "REMAINING", "LIMIT", "CONFIDENCE")
	fmt.Fprintln(a.Out, strings.Repeat("-", 78))
	for _, s := range rows {
		fmt.Fprintf(a.Out, "%-12s %-14s %-12s %-16s %-12s %s\n",
			s.Account.Engine, s.Account.Account, colorState(string(s.State)),
			quota.FormatRemaining(s), quota.FormatLimit(s), quota.ConfidenceTag(s.Confidence))
	}
	fmt.Fprintln(a.Out)
	fmt.Fprintln(a.Out, dimPlain+"Estimated/inferred values are shown with a '~' prefix; EXACT/PROVIDER_REPORTED without."+reset)
	return ExitOK
}

const (
	boldPlain = "\x1b[1m"
	dimPlain  = "\x1b[2m"
	reset     = "\x1b[0m"
)

func colorState(s string) string {
	switch s {
	case "AVAILABLE":
		return "\x1b[32m" + s + reset
	case "LOW", "DEGRADED":
		return "\x1b[33m" + s + reset
	case "RATE_LIMITED":
		return "\x1b[35m" + s + reset
	case "EXHAUSTED", "AUTH_REQUIRED":
		return "\x1b[31m" + s + reset
	}
	return dimPlain + s + reset
}
