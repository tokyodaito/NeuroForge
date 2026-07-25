package cli

import (
	"io"
	"testing"

	"neuroforge/internal/bootstrap"
)

// InitDepsForTest wires fake bootstrap components so `forge init`/`update` run
// fully offline and deterministically (rule §33). It is intended for tests.
func (a *App) InitDepsForTest(tb testing.TB, detector bootstrap.Detector, installerOut io.Writer) {
	tb.Helper()
	a.initDeps = &initDependencies{
		detector: func() bootstrap.Detector { return detector },
		installer: func(out io.Writer) bootstrap.Installer {
			if installerOut != nil {
				return newGuidedInstaller(installerOut)
			}
			return newGuidedInstaller(out)
		},
		confirmer: func(in io.Reader, out io.Writer, yes bool) bootstrap.Confirmer {
			return bootstrap.NewAutoConfirmer(true, true, true)
		},
	}
}
