package bootstrap

import (
	"context"
	"fmt"
)

// AuthStatus is the per-provider authentication outcome (§7.2 stage 6).
type AuthStatus string

const (
	AuthConnected   AuthStatus = "connected"
	AuthLoginNeeded AuthStatus = "login_required"
	AuthConfigure   AuthStatus = "configure_models"
	AuthSkipped     AuthStatus = "skipped"
	AuthFailed      AuthStatus = "failed"
)

// AuthEntry is one provider's auth row in the §7.2 stage-6 table.
type AuthEntry struct {
	Provider string
	Status   AuthStatus
	Detail   string
}

// LoginLauncher launches a provider's OFFICIAL authentication mechanism. It MUST
// NOT request the provider password inside NeuroForge (§7.2 stage 6:
// "НейроФоржде не должен просить пользователя вводить пароль provider
// непосредственно в NeuroForge"). It delegates to the provider's own CLI/web
// flow. Tests inject a fake.
type LoginLauncher interface {
	// LaunchOfficialLogin opens the provider's official login flow and reports
	// the outcome. The password is NEVER collected by NeuroForge.
	LaunchOfficialLogin(ctx context.Context, providerID string) (AuthStatus, error)
}

// AuthWizard runs stage 6 of onboarding (§7.2 stage 6). It never collects a
// password; it only launches each provider's official mechanism and records the
// outcome.
type AuthWizard struct {
	launcher LoginLauncher
}

// NewAuthWizard builds the wizard. launcher may be nil — the wizard then records
// every provider as login_required (it never fabricates a connection).
func NewAuthWizard(launcher LoginLauncher) *AuthWizard {
	return &AuthWizard{launcher: launcher}
}

// Run authenticates the providers in the plan. It only attempts login for tools
// that have an ActionAuth step.
func (w *AuthWizard) Run(ctx context.Context, plan *InstallPlan) []AuthEntry {
	var entries []AuthEntry
	for _, step := range plan.WillInstall {
		if step.Action != ActionAuth {
			continue
		}
		entry := AuthEntry{Provider: step.ToolID, Status: AuthLoginNeeded}
		if w.launcher == nil {
			entries = append(entries, entry)
			continue
		}
		st, err := w.launcher.LaunchOfficialLogin(ctx, step.ToolID)
		if err != nil {
			entry.Status = AuthFailed
			entry.Detail = fmt.Sprintf("official login failed: %v (no password was collected)", err)
			entries = append(entries, entry)
			continue
		}
		entry.Status = st
		entries = append(entries, entry)
	}
	return entries
}

// RenderAuthTable renders the §7.2 stage-6 table.
func RenderAuthTable(entries []AuthEntry) string {
	out := ""
	for _, e := range entries {
		detail := e.Detail
		if detail == "" {
			switch e.Status {
			case AuthConnected:
				detail = "Connected"
			case AuthLoginNeeded:
				detail = "Login required"
			case AuthConfigure:
				detail = "Configure models"
			case AuthSkipped:
				detail = "Skipped"
			}
		}
		out += fmt.Sprintf("%-16s %s\n", e.Provider, detail)
	}
	return out
}
