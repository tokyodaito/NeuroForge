package grok

import (
	"context"
	"errors"
	"os"
	"os/exec"
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

// lookPath finds the Grok executable via [os/exec.LookPath]. A name that is
// already a path (absolute or relative) is validated directly.
func lookPath(name string) (string, error) {
	if name == "" {
		return "", errors.New("grok: empty binary name")
	}

	// Absolute or relative path: resolve directly.
	if strings.ContainsRune(name, os.PathSeparator) || strings.ContainsRune(name, '/') {
		if fileExists(name) {
			return name, nil
		}
		return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
	}

	// Bare name: search PATH.
	return exec.LookPath(name)
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
