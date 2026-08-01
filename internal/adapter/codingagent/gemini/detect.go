package gemini

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// Detect implements [codingagent.Adapter]. It resolves the Gemini CLI on PATH
// and, when found, records its version via a `gemini --version` probe.
// Detection never makes a paid call.
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

// lookPath resolves name by searching each PATH directory for an executable
// file. It tolerates spaces/Unicode in PATH entries (Go paths are UTF-8) and
// requires the executable bit to be set (Unix semantics).
//
// Behaviour:
//
//   - If name already carries a path separator, it is checked directly.
//   - Otherwise each PATH directory is searched for the bare name.
func lookPath(name string) (string, error) {
	if name == "" {
		return "", errors.New("empty executable name")
	}

	// Direct path / already-qualified name.
	if strings.ContainsRune(name, os.PathSeparator) || strings.ContainsRune(name, '/') {
		return statExecutable(name)
	}

	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			dir = "."
		}
		if p, err := statExecutable(filepath.Join(dir, name)); err == nil {
			return p, nil
		}
	}
	return "", execNotFound(name)
}

// statExecutable reports whether path is a usable executable: a regular file
// executable by the current user.
func statExecutable(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", errors.New("is a directory")
	}
	if info.Mode()&0o111 == 0 {
		return "", errors.New("not executable")
	}
	return path, nil
}

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
