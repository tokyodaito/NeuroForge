# ADR-0013: Visual verification harness protocol

- **Status:** Accepted
- **Date:** 2026-07-25
- **Spec refs:** §16 (Visual Verification Engine), §15.2 (design-to-code flow),
  §33.3 (fake visual harness), §36.9 (separation of concerns)

## Context

The visual verification engine (§16) must obtain a real screenshot of the UI
produced by a coding agent and compare it against the locked visual
specification (§15.6) or, when no reference exists, run a reference-free review
(§16.6). The spec mandates a command-based generic harness as the first
implementation (§16.2: "Поддержать command-based generic harness") plus a
first-class Android harness (emulator, AVD, APK install, Activity launch,
locale, theme, font scale, fixed resolution, screenshot). A web harness is
deferred to a later milestone (§16.2).

The harness is a separate concern from both coding agents (§12) and image
providers (§14): it captures a screenshot of an already-built app, it does not
generate code or images. Like the other adapter families, adding a harness must
be purely additive (rule §13.3 applies by analogy): registering a harness must
not change the visual engine, scheduler, schema or dashboard.

## Decision

Define `VisualHarness` in `internal/adapter/visualharness` covering the §16.1
surface: `Detect`, `Build`, `Launch`, `Navigate`, `Capture`, `Shutdown` plus
`ClassifyFailure` (mirroring the coding-agent and image-provider adapter
discipline). Concrete harnesses:

- `internal/adapter/visualharness/generic` — the command-based harness (§16.2
  mandatory first implementation); Build/Launch/Capture are configurable shell
  commands declared in project config.
- `internal/adapter/visualharness/android` — the first-class Android harness
  (§16.2); wraps `adb`/`emulator` for AVD selection, APK install, Activity
  launch, locale/theme/font-scale/fixed-resolution, and `screencap` capture.
- `internal/adapter/visualharness/fake` — the §33.3 fake harness (matching,
  mismatch, blank screen, clipped UI, startup failure); deterministic, used by
  all CI tests so no real device is required (rule §33).

A `Registry` mirrors the coding-agent and image-provider registries. Captured
screenshots are content-addressed via the artifact store (§9.5, ADR-0014) so
deterministic comparison is byte-stable. Harnesses run against an isolated
worktree and MUST NOT touch the user's primary checkout (§17.1) or perform
network delivery actions (§29).

The engine (`internal/visual`) consumes the harness output and produces §16.4
results. AC-24 is enforced at the engine boundary: a `Status` of `skipped` or
`not_verified` is NEVER claimable as "verified" (`Status.IsVerified()` returns
true only for `passed`).

## Consequences

**Positive**

- Clean separation: a new harness (e.g. web via browser automation) lands
  without touching the engine or schema.
- Deterministic CI: the fake harness lets the full repair loop run without a
  device (rule §33).
- The §16.6 invariant (no pixel-perfect claim without a reference) is encoded
  in the type system (`ReferenceBased`, `PixelPerfect`, `Mode`).

**Negative / trade-offs**

- A third adapter family adds conformance surface; mitigated by reusing the §32
  taxonomy and the registry/eventsink discipline.

## Alternatives considered

- **Fold the harness into the image-provider adapter.** Rejected: §16 is a
  distinct concern (capture vs generate) and conflating them would break
  quota/budget accounting and the §36.9 separation.
- **Build the Android harness first, skip the generic one.** Rejected: §16.2
  explicitly mandates the command-based generic harness as the first
  implementation; the Android harness is additive.
