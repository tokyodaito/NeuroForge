package grok

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// resolveBinary returns the Grok executable path to use (Options.Binary, or the
// default name). It does not validate existence.
func (a *Adapter) resolveBinary() string {
	if a.opts.Binary != "" {
		return a.opts.Binary
	}
	return defaultBinaryName
}

// lookPath finds the Grok executable. It is a PATHEXT-aware superset of
// [os/exec.LookPath]: on Windows it tolerates .exe/.cmd/.bat and npm/cjs shims
// and respects a possibly custom PATHEXT; on every platform it tolerates spaces
// and Unicode in both the name and PATH entries. A name that is already a path
// (absolute or relative) is resolved directly with extension trials on Windows.
func lookPath(name string) (string, error) {
	if name == "" {
		return "", errors.New("grok: empty binary name")
	}

	// Absolute or relative path: resolve directly.
	if strings.ContainsRune(name, os.PathSeparator) || strings.ContainsRune(name, '/') {
		return resolvePathWithExtensions(name)
	}

	// Bare name: search PATH. exec.LookPath already honours PATHEXT on Windows
	// (Go ≥ 1.19), but we fall back to a manual search so a custom PATHEXT,
	// missing extensions and shim scripts are all handled uniformly.
	if runtime.GOOS == "windows" {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return searchPath(name)
}

// resolvePathWithExtensions handles a name that is already a path, applying
// Windows extension trials when it has no extension.
func resolvePathWithExtensions(name string) (string, error) {
	if fileExists(name) {
		return name, nil
	}
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		for _, ext := range pathExts() {
			candidate := name + ext
			if fileExists(candidate) {
				return candidate, nil
			}
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// searchPath walks each PATH entry and tries the bare name plus PATHEXT
// extensions (Windows) or the exact name (other platforms).
func searchPath(name string) (string, error) {
	pathEnv := os.Getenv("PATH")
	exts := []string{""}
	if runtime.GOOS == "windows" {
		exts = pathExts()
		if len(exts) == 0 {
			exts = []string{".exe", ".cmd", ".bat"}
		}
		// Allow case-insensitive match of the bare name too.
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			continue
		}
		for _, ext := range exts {
			candidate := filepath.Join(dir, name+ext)
			if fileExists(candidate) {
				return candidate, nil
			}
		}
	}
	// Final fallback: let exec.LookPath decide (covers Windows case where the
	// manual search missed a PATHEXT-less match found by the stdlib).
	if runtime.GOOS != "windows" {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// pathExts returns the ordered PATHEXT extensions (Windows), each lower-cased
// for matching but returned verbatim for appending. Empty off-Windows.
func pathExts() []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	pe := os.Getenv("PATHEXT")
	if pe == "" {
		pe = ".COM;.EXE;.BAT;.CMD;.VBS;.VBE;.JS;.JSE;.WSF;.WSH;.MSC"
	}
	parts := strings.Split(pe, ";")
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

func fileExists(name string) bool {
	info, err := os.Stat(name)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return false
	}
	return true
}

// runVersionProbe executes `<binary> --version` and returns its combined output
// and exit status. It is the single place that invokes the Grok CLI for
// metadata. It honours a short timeout so a missing/hung binary never blocks
// detection.
func runVersionProbe(ctx context.Context, binary string) (string, int, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resolved, err := lookPath(binary)
	cmd := exec.CommandContext(probeCtx, resolved, "--version")
	out, err := cmd.CombinedOutput()
	exit := exitCodeOf(err)
	if err != nil && out == nil {
		return "", exit, err
	}
	return strings.TrimSpace(string(out)), exit, nil
}

// Detect implements codingagent.Adapter. It resolves the Grok binary, probes its
// version, and reports presence/usable. Unknown/unparseable versions are still
// considered "installed" if the binary runs (spec §12.2: Detect reports presence
// + usability, not version validity).
func (a *Adapter) Detect(ctx context.Context) protocol.DetectionResult {
	bin := a.resolveBinary()
	resolved, err := lookPath(bin)
	if err != nil {
		return protocol.DetectionResult{
			Installed: false,
			Detail:    "grok binary not found on PATH (lookPath: " + err.Error() + ")",
		}
	}
	out, _, runErr := runVersionProbe(ctx, bin)
	if runErr != nil && out == "" {
		return protocol.DetectionResult{
			Installed: false,
			Path:      resolved,
			Detail:    "grok --version failed: " + runErr.Error(),
		}
	}
	v := parseVersion(out)
	a.mu.Lock()
	a.cachedVersion = v
	a.cachedVersionRaw = out
	a.mu.Unlock()
	detail := "grok detected"
	if v.known {
		detail = "grok " + v.String() + " detected"
	}
	return protocol.DetectionResult{
		Installed: true,
		Path:      resolved,
		Version:   out,
		Detail:    detail,
	}
}
