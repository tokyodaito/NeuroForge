# Running NeuroForge under WSL2 (Windows hosts)

NeuroForge is Linux-only. There is no native Windows build: the supported way
to run it from a Windows host is **WSL2** (Windows Subsystem for Linux). All
commands below run **inside the WSL2 distribution**, never in PowerShell or
`cmd.exe`.

## Supported setup

- WSL2 with a mainstream Linux distribution (**Ubuntu** recommended).
- NeuroForge, its daemon, and every coding-agent CLI run as Linux processes
  inside the WSL2 distro.
- macOS may keep working via the generic Unix code paths, but it receives no
  dedicated support; Linux (native or WSL2) is the canonical target.

## Repository location

Clone the repository into the **Linux filesystem**, e.g. `~/code/neuroforge`:

```sh
mkdir -p ~/code && cd ~/code
git clone <repo-url> neuroforge
cd neuroforge
```

Do **not** work from a Windows-mounted path (`/mnt/c/...`): file access there
is slow, and permission/exec-bit handling causes subtle build, test and Git
problems.

## Install

Inside WSL2 (Ubuntu):

```sh
sudo apt update
sudo apt install -y git golang-go   # or install Go ≥ 1.23 from https://go.dev/dl/
```

Go **≥ 1.23** is required. Then build:

```sh
make build        # produces ./forge
# or, without make:
go build -o forge ./cmd/forge
./forge version
```

## Daemon startup

Start and inspect the daemon exactly as on native Linux:

```sh
./forge daemon start
./forge daemon status
```

The daemon listens on loopback only; the CLI and TUI reach it over the
token-protected HTTP+SSE transport. All state lives under
`~/.neuroforge/` inside the WSL2 distro.

## Git and SSH

Configure Git **inside WSL2** — NeuroForge shells out to the Linux `git` and
never sees the Windows-side configuration:

```sh
git config --global user.name "Your Name"
git config --global user.email "you@example.com"
```

If you push over SSH, generate or copy a key into `~/.ssh/` inside WSL2 and
register it with your Git host. Keep Windows-side and WSL2-side keys separate;
do not reuse credentials across the boundary.

## OpenCode inside WSL2

The default coding-agent adapter is OpenCode. Install the Linux build inside
WSL2 with the official installer from <https://opencode.ai>:

```sh
curl -fsSL https://opencode.ai/install | bash
```

Then authenticate inside WSL2. With a Z.ai Coding Plan:

```sh
opencode   # run once and complete the Z.ai Coding Plan login when prompted
```

Authentication state is stored at `~/.local/share/opencode/auth.json` inside
the WSL2 filesystem.

NeuroForge discovers OpenCode via a `PATH` lookup for the `opencode` binary.
Optionally, an explicit binary path can be set in the adapter options (see
`docs/adapters/opencode.md`).

## Warnings

- Read the [security model](../../README.md#security-model) first: the agent
  runs unsandboxed as your user (with `HOME` access, which OpenCode auth
  requires), the worktree is not a security boundary, and review is a quality
  gate — not an adversarial one.
- **Never copy OpenCode auth files** (`~/.local/share/opencode/auth.json`)
  into the repository or into any worktree. They are credentials.
- Run NeuroForge and OpenCode as the **same Linux user**; mixing users breaks
  auth-file discovery and file ownership.
- Do **not** run NeuroForge or OpenCode under `sudo`: it changes `HOME` and
  silently breaks OpenCode authentication and the daemon's per-user state.
- Avoid Windows-mounted paths (`/mnt/c`, `/mnt/d`) for the repository,
  worktrees and `~/.neuroforge` — they are slow and have unreliable
  permission semantics.
