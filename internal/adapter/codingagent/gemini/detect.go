package gemini

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// Detect implements [codingagent.Adapter]. It resolves the Gemini CLI on PATH
// (honouring PATHEXT and npm shims on Windows) and, when found, records its
// version via a `gemini --version` probe. Detection never makes a paid call.
//
// The probe is best-effort: a binary present but failing --version is still
// reported as Installed (with the error in Detail), so `forge agent doctor` can
// surface the diagnostic rather than silently treating the engine as absent.
func (a *Adapter) Detect(ctx context.Context) protocol.DetectionResult {
	name := a.opts.Binary
	resolved, err := a.host.lookPath(name)
	if err != nil {
		return protocol.DetectionResult{
			Installed: false,
			Detail:    "gemini CLI not found on PATH: " + err.Error(),
		}
	}

	version, versionDetail, versionErr := a.probeVersion(ctx, resolved)
	detail := "gemini CLI detected at " + resolved
	if version != "" {
		detail += " (version " + version + ")"
	}

	// A failed --version probe still counts as installed: the binary exists and
	// is executable; the supervisor can surface the version error separately.
	return protocol.DetectionResult{
		Installed: true,
		Path:      resolved,
		Version:   version,
		Detail:    joinDetail(detail, versionDetail, versionErr),
	}
}

// probeVersion runs `<binary> --version` and returns the parsed version string.
func (a *Adapter) probeVersion(ctx context.Context, binary string) (version, detail string, err error) {
	env := versionProbeEnv()
	stdout, stderr, runErr := a.host.probe(ctx, []string{binary, "--version"}, env)
	if runErr != nil {
		// Some CLIs write the version to stderr; accept either stream.
		combined := stdout
		if combined == "" {
			combined = stderr
		}
		if v, perr := parseGeminiVersion(combined); perr == nil && v.raw != "" {
			return v.String(), "version parsed from probe output despite non-zero exit", runErr
		}
		return "", "", runErr
	}
	v, perr := parseGeminiVersion(stdout)
	if perr != nil {
		return "", "could not parse version from --version output: " + perr.Error(), perr
	}
	return v.String(), "", nil
}

func joinDetail(parts ...any) string {
	var b strings.Builder
	for _, p := range parts {
		switch v := p.(type) {
		case string:
			if v == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("; ")
			}
			b.WriteString(v)
		case error:
			if v == nil {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("; ")
			}
			b.WriteString(v.Error())
		}
	}
	return b.String()
}

// lookPath resolves name using an explicit PATHEXT-aware search so detection is
// deterministic across platforms and tolerant of spaces/Unicode in PATH. It is
// a superset of [os/exec.LookPath] that also rejects PowerShell-only shims
// (.ps1) which cannot be spawned by os/exec on Windows.
//
// Behaviour:
//
//   - If name already carries a path separator or a recognised executable
//     suffix, it is checked directly (after appending a Windows extension when
//     the OS is windows and none is present).
//   - Otherwise each PATH directory is searched; on Windows every PATHEXT
//     extension is tried in declared order. Directories with spaces and Unicode
//     are handled naturally (Go paths are UTF-8).
//   - Files ending in ".ps1" are skipped on Windows: npm installs both a .cmd
//     shim and a .ps1 script; only the .cmd shim is directly executable via
//     CreateProcess, so the .cmd is preferred.
func lookPath(name string) (string, error) {
	if name == "" {
		return "", errors.New("empty executable name")
	}

	// Direct path / already-qualified name.
	if strings.ContainsRune(name, os.PathSeparator) || strings.ContainsRune(name, '/') {
		return findDirect(name)
	}

	pathEnv := os.Getenv("PATH")
	paths := filepath.SplitList(pathEnv)
	exts := pathExts()

	for _, dir := range paths {
		if dir == "" {
			dir = "."
		}
		base := name
		// On Windows, if the candidate already has an extension, try it as-is
		// first before cycling through PATHEXT (covers "gemini.cmd").
		if hasExt(name) {
			if p, err := statExecutable(filepath.Join(dir, base)); err == nil {
				return p, nil
			}
		}
		for _, ext := range exts {
			candidate := filepath.Join(dir, base+ext)
			if p, err := statExecutable(candidate); err == nil {
				return p, nil
			}
		}
	}
	return "", execNotFound(name)
}

// findDirect handles a name that already contains a path separator.
func findDirect(name string) (string, error) {
	if p, err := statExecutable(name); err == nil {
		return p, nil
	}
	for _, ext := range pathExts() {
		if hasExt(name) {
			break
		}
		if p, err := statExecutable(name + ext); err == nil {
			return p, nil
		}
	}
	return "", execNotFound(name)
}

// statExecutable reports whether path is a usable executable. On Windows any
// regular file with an executable suffix matches (PATHEXT governs); on Unix the
// file must be executable by the current user.
func statExecutable(path string) (string, error) {
	if isPowerShellOnlyShim(path) {
		return "", errSkipShim
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", errors.New("is a directory")
	}
	if runtime.GOOS != "windows" {
		if info.Mode()&0o111 == 0 {
			return "", errors.New("not executable")
		}
	}
	return path, nil
}

// isPowerShellOnlyShim reports whether path is a .ps1 script. Such scripts
// cannot be launched by os/exec on Windows (CreateProcess does not run
// PowerShell), so they are skipped in favour of the .cmd shim npm installs
// alongside them.
func isPowerShellOnlyShim(path string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	return strings.EqualFold(filepath.Ext(path), ".ps1")
}

// hasExt reports whether name has any extension (a dot in its last element).
func hasExt(name string) bool {
	base := filepath.Base(name)
	return strings.ContainsRune(base, '.')
}

// pathExts returns the ordered executable extensions to try. On Windows this is
// PATHEXT (defaulting to a sane ordering when unset); elsewhere it is empty
// (Unix resolves executable bits, not extensions).
func pathExts() []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	pathext := os.Getenv("PATHEXT")
	if pathext == "" {
		pathext = ".COM;.EXE;.BAT;.CMD;.VBS;.JS;.WS;.MSC"
	}
	parts := strings.Split(pathext, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// errSkipShim marks a skipped PowerShell-only shim.
var errSkipShim = errors.New("skip powershell-only shim")

// execNotFound builds a not-found error mirroring os/exec's ErrNotFound style.
func execNotFound(name string) error {
	return &notFoundError{name: name}
}

type notFoundError struct{ name string }

func (e *notFoundError) Error() string { return "exec: " + quote(e.name) + ": not found" }

// quote wraps name in double quotes for the error message (filepath names may
// contain spaces).
func quote(name string) string {
	return "\"" + name + "\""
}
