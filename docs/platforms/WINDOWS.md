# Building and running NeuroForge on Windows

NeuroForge is developed and tested on Windows as a first-class platform. The
daemon, SQLite storage, audit trail, loopback transport, TUI, workspace manager
and the fake coding agent all run on Windows. This guide covers everything from
prerequisites to a clean uninstall.

> The authoritative requirements live in
> [`../spec/NEUROFORGE_SPEC.md`](../spec/NEUROFORGE_SPEC.md). When this guide and
> the spec disagree, the spec wins.

---

## Prerequisites

| Tool | Required? | Notes |
|------|-----------|-------|
| **Go** >= 1.23 (module pins `go 1.26.5`) | Required | Install explicitly — NeuroForge never installs toolchains silently (spec §36.17). Use `winget install GoLang.Go` or <https://go.dev/dl/>. |
| **Git for Windows** | Required | `winget install Git.Git` or <https://git-scm.com/>. |
| **C compiler (gcc)** | **Not required** | The SQLite driver is pure-Go (`modernc.org/sqlite`, ADR-0010). Production builds are `CGO_ENABLED=0` and need no compiler. A compiler is only needed for the optional race detector (see below). |
| **make** | Not required | The `scripts/*.ps1` PowerShell scripts are the first-class path on Windows. The POSIX `Makefile` also works if you have make. |
| **PowerShell 7 (pwsh)** | Optional | The scripts run on Windows PowerShell 5.1 (preinstalled). PS 7 is a nice-to-have. |
| **Windows Terminal** | Recommended | Better ANSI/mouse/resize support for the TUI than the legacy `conhost`. |

Run `powershell -NoProfile -File scripts/doctor.ps1` to verify your environment.

### Supported Windows versions

Windows 10 (1809+) and Windows 11, x64. **Enable long path support** to avoid
`MAX_PATH` issues with deeply-nested worktrees:

```powershell
# Run once from an elevated PowerShell (this is a system setting):
New-ItemProperty -Path 'HKLM:\SYSTEM\CurrentControlSet\Control\FileSystem' `
  -Name LongPathsEnabled -Value 1 -PropertyType DWord -Force
```

`scripts/doctor.ps1` reports whether long paths are enabled.

---

## Clone and build

```powershell
git clone https://github.com/<owner>/NeuroForge neuroforge
cd neuroforge
git checkout platform/windows-bootstrap   # the Windows-prep branch

# Build ./forge.exe with version metadata injected (like `make build`):
powershell -NoProfile -File scripts/dev.ps1

# Print version / commit / platform:
.\forge.exe version
```

`scripts/dev.ps1` works from any directory and finds the repo root itself. To
build *and* run:

```powershell
powershell -NoProfile -File scripts/dev.ps1 -Run version
powershell -NoProfile -File scripts/dev.ps1 -Run help
```

---

## Tests and the CI gate

`scripts/check.ps1` is the Windows equivalent of `make check`
(gofmt check + `go vet` + `go test`). It requires neither `make` nor a compiler,
never installs anything, and exits non-zero on any failure:

```powershell
powershell -NoProfile -File scripts/check.ps1
```

Other scripts:

```powershell
powershell -NoProfile -File scripts\test.ps1               # go test ./...
powershell -NoProfile -File scripts\test.ps1 -Race -Verbose # go test -race -v ./...
powershell -NoProfile -File scripts\doctor.ps1              # environment diagnostics
```

### Race detector (optional)

`go test -race` requires cgo and therefore a C compiler. It is **not** needed for
normal development (production builds are pure-Go). If you want to run it
locally, install MinGW and enable cgo:

```powershell
winget install MartinStuder.WinLibs.GCC.Mingw-w64   # or another MinGW
$env:CGO_ENABLED = '1'
go test -race ./...
```

If no compiler is present, `go test -race` fails with
`-race requires cgo; enable cgo by setting CGO_ENABLED=1` — this is expected, not
a NeuroForge bug. The race detector is always run in CI (Linux job unconditionally;
Windows job where the runner provides MinGW).

### Line endings

A `.gitattributes` pins all source/config to **LF** (gofmt is LF-sensitive). If
your machine has `core.autocrlf=true`, Git still checks `.go` files out with LF
because `.gitattributes` overrides it, so `scripts/check.ps1` stays clean. Do not
commit CRLF into `.go` files.

---

## PowerShell execution policy

Windows may restrict running `.ps1` files. Do **not** globally disable execution
policy. Run a script for the current process only:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\check.ps1
```

`-ExecutionPolicy Bypass` applies only to that one `powershell.exe` invocation and
never changes the machine or user policy.

---

## Running the daemon

The daemon owns all mutable state (ADR-0002). Both the CLI and the TUI reach it
through an authenticated **loopback** HTTP API (never a Unix socket, never a
non-loopback interface).

```powershell
$env:NEUROFORGE_HOME = "$env:USERPROFILE\.neuroforge"   # optional override

.\forge.exe daemon run      # run in the foreground (blocks)
.\forge.exe daemon start    # start a detached background daemon
.\forge.exe daemon status   # show lifecycle status
.\forge.exe daemon logs     # tail structured JSON logs
.\forge.exe daemon stop     # graceful stop (/shutdown, then kill)
```

`forge daemon start` spawns the daemon detached (its own process group,
`CREATE_NO_WINDOW`), so no extra console window appears and it survives the CLI
exiting. Stop is graceful: the CLI posts `/shutdown` to the loopback API, then —
only as a fallback — terminates the process. There is no `SIGTERM` on Windows;
shutdown is driven by Ctrl+C / `/shutdown`.

A repeated `daemon start` never spawns a second daemon: a single-instance guard
checks the PID file via `OpenProcess` and refuses if a healthy owner exists;
stale PID files (crashed daemon) are reclaimed automatically.

---

## Running the TUI

```powershell
.\forge.exe            # interactive TUI (or: .\forge.exe dashboard)
```

The TUI uses an alternate-screen raw mode (`golang.org/x/term`, ADR-0011) and
degrades gracefully on a non-TTY. **Windows Terminal** is recommended for the
best keyboard/mouse/resize/ANSI support. Press `q`, `Esc` or `Ctrl-C` to quit.

---

## File locations

| Purpose | Path |
|---------|------|
| Runtime root | `%NEUROFORGE_HOME%` (default `%USERPROFILE%\.neuroforge`) |
| SQLite database | `<root>\state.db` |
| Daemon logs | `<root>\logs\daemon.log` |
| Runtime metadata (pid/token/addr) | `<root>\run\` |
| Content-addressed attachments | `<root>\artifacts\` |
| Managed git worktrees | `<root>\workspaces\` |
| Go module cache | `%GOPATH%\pkg\mod` (default `%USERPROFILE%\go\pkg\mod`) |
| Go build cache | `%LOCALAPPDATA%\go-build` |

Set `NEUROFORGE_HOME` to redirect all runtime state (useful for tests/isolation).

### File permissions note (important)

Unix permission bits (`0600` files / `0700` dirs) are **not** a Windows concept.
On POSIX, the runtime files are `0600` and dirs `0700` (owner-only, enforced).
On Windows, `os.WriteFile(..., 0o600)` / `os.Chmod` are effectively no-ops and
files are created with the default DACL; NeuroForge **does not** claim a Unix
mode guarantee on Windows (no false security claim, spec §36.25). Runtime privacy
on Windows rests on the directory living under your user profile
(`%USERPROFILE%\.neuroforge`), which the default ACL keeps owner-private. If you
need stronger isolation, point `NEUROFORGE_HOME` at an encrypted/ACL-locked
location.

---

## Known limitations on Windows

- **No SIGTERM.** Graceful shutdown uses the loopback `/shutdown` endpoint and
  Ctrl+C; `os.Process.Kill` is the last resort. Signal-based `terminateProcess`
  is intentionally a no-op here (see `internal/daemon/process_windows.go`).
- **Race detector needs a compiler.** See above.
- **File mode bits not enforced.** See the permissions note.
- **Long paths.** Enable `LongPathsEnabled=1` (recommended) to avoid issues with
  deeply-nested worktree paths.

Everything else (storage, audit, transport, workspaces, supervisor, the fake
agent, the conformance suite) is platform-independent and fully functional.

---

## Troubleshooting

**`go` / `gofmt` not recognized after installing Go.**
A newly installed Go updates the Machine/User PATH but not already-open
processes. Open a new terminal, or run `scripts/doctor.ps1` (it refreshes PATH
from the registry). NeuroForge scripts refresh PATH themselves automatically.

**`gofmt would reformat: <every file>` in `check.ps1`.**
Your working tree has CRLF line endings. `.gitattributes` prevents this on fresh
clones; re-clone, or run `git add --renormalize .` and check out again.

**`forge daemon start` says another daemon owns the runtime.**
A previous daemon crashed and left a stale PID. Run `forge daemon stop` (it
reclaims stale state) or `forge daemon start` again (reclaim is automatic).

**A detached daemon console window pops up.**
`forge daemon start` uses `CREATE_NO_WINDOW`; if you still see one, you are
running an older build — rebuild with `scripts/dev.ps1`.

**`taskkill`-related errors during agent cancellation.**
Process-group cancellation uses `taskkill /T /F`. It is best-effort and tolerant
of a process that already exited (matching the Unix `ESRCH` behaviour), so
spurious errors during a race are suppressed.

---

## Clean uninstall of local runtime artifacts

NeuroForge never modifies your system beyond its own runtime directory. To remove
all local state:

```powershell
# 1. Stop the daemon if it is running.
.\forge.exe daemon stop

# 2. Remove the runtime directory (database, logs, worktrees, artifacts).
$root = if ($env:NEUROFORGE_HOME) { $env:NEUROFORGE_HOME } else { Join-Path $env:USERPROFILE '.neuroforge' }
Remove-Item -Recurse -Force $root

# 3. (Optional) remove the built binary and the repository.
Remove-Item -Force forge.exe
# rm -r the cloned repository when you are done.
```

This deletes all per-user state but leaves your Go toolchain, Git and the OS
untouched.
