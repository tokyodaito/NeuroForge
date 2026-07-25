//go:build windows

package bootstrap

// isElevated on Windows is conservatively false. NeuroForge never performs
// privilege escalation automatically (§36.18) — the confirmer gates any
// elevated step.
func isElevated() bool { return false }
