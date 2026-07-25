package cli

import (
	"io"

	"neuroforge/internal/bootstrap"
)

// initDependencies allows tests to inject deterministic bootstrap components
// (detector, installer, confirmer) so `forge init` is testable offline without
// shelling out (rule §33). When a field is nil, runInit builds the real
// production component bound to the App's streams.
type initDependencies struct {
	detector  func() bootstrap.Detector
	installer func(out io.Writer) bootstrap.Installer
	confirmer func(in io.Reader, out io.Writer, yes bool) bootstrap.Confirmer
}

// resolveInitDeps returns the dependency factories, defaulting to the real ones.
func (a *App) resolveInitDeps() initDependencies {
	if a.initDeps != nil {
		return *a.initDeps
	}
	return initDependencies{
		detector:  func() bootstrap.Detector { return bootstrap.NewCommandDetector() },
		installer: func(out io.Writer) bootstrap.Installer { return newGuidedInstaller(out) },
		confirmer: func(in io.Reader, out io.Writer, yes bool) bootstrap.Confirmer {
			return bootstrap.NewTTYConfirmer(in, out, yes)
		},
	}
}
