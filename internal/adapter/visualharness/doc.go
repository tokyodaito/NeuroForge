// Package visualharness defines the visual verification harness protocol and
// registry (spec §16, ADR pending).
//
// STATUS: implemented for milestone M10.
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §16):
//
//   - [Harness] interface (Detect/Build/Launch/Navigate/Capture/Shutdown +
//     ClassifyFailure).
//   - The command-based generic harness (§16.2: "Поддержать command-based
//     generic harness") in sub-package [generic].
//   - The first-class Android harness (§16.2: emulator, AVD, APK install,
//     Activity launch, locale, theme, font scale, fixed resolution,
//     screenshot) in sub-package [android].
//   - The fake visual harness (§33.3: matching/mismatch/blank/clipped/startup
//     failure) in sub-package [fake].
//
// Boundaries (§17.1, §29): harnesses run against an isolated build/worktree;
// they MUST NOT touch the user's primary checkout or perform network delivery
// actions. The captured screenshot is content-addressed via the artifact store
// (§9.5).
package visualharness
