// Package visualharness defines the visual verification harness protocol.
//
// STATUS: scaffold — not implemented (planned for milestone M10).
//
// Scope (docs/spec/NEUROFORGE_SPEC.md §16): the VisualHarness interface
// (Detect/Build/Launch/Navigate/Capture/Shutdown), the command-based generic
// harness and the first-class Android harness (emulator/AVD/APK/Activity/locale/
// theme/font-scale/resolution/screenshot), plus deterministic and multimodal
// verification and the repair loop.
//
// Boundaries: harnesses run against an isolated build/worktree; they must not
// touch the user's primary checkout or perform network delivery actions.
package visualharness
