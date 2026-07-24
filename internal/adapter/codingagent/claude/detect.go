package claude

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// compile-time assertion that *Adapter satisfies the full coding-agent Adapter
// interface (spec §12.2: all 13 methods).
var _ codingagent.Adapter = (*Adapter)(nil)

// semverRe matches a leading or embedded semantic version major.minor.patch
// (patch optional). Claude Code prints versions like "2.1.205 (Claude Code)".
var semverRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// parsedVersion is a decoded Claude Code CLI semver.
type parsedVersion struct {
	Major, Minor, Patch int
	Full                string
}

// parseVersion extracts the first major.minor.patch from s. Returns ok=false
// when no version is present (the engine is treated as unknown-age).
func parseVersion(s string) (parsedVersion, bool) {
	m := semverRe.FindStringSubmatch(s)
	if m == nil {
		// Fall back to major.minor.
		re := regexp.MustCompile(`(\d+)\.(\d+)`)
		if mm := re.FindStringSubmatch(s); mm != nil {
			maj, _ := strconv.Atoi(mm[1])
			min, _ := strconv.Atoi(mm[2])
			return parsedVersion{Major: maj, Minor: min, Full: mm[0]}, true
		}
		return parsedVersion{}, false
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	pat, _ := strconv.Atoi(m[3])
	return parsedVersion{Major: maj, Minor: min, Patch: pat, Full: m[0]}, true
}

// atLeast reports whether v is >= the requested major.minor.patch.
func (v parsedVersion) atLeast(maj, min, pat int) bool {
	if v.Major != maj {
		return v.Major > maj
	}
	if v.Minor != min {
		return v.Minor > min
	}
	return v.Patch >= pat
}

// binary resolves the executable to invoke: Options.BinaryPath when set,
// otherwise "claude" via the injected PATHEXT-aware LookPath.
func (a *Adapter) binary() (string, error) {
	if p := strings.TrimSpace(a.opts.BinaryPath); p != "" {
		// An explicit path is trusted as-is (callers pass an absolute path).
		return p, nil
	}
	return a.opts.LookPath(defaultBinaryName)
}

// Detect implements codingagent.Adapter. It resolves the CLI and runs
// `claude --version`, tolerating a missing version probe (the binary is still
// "installed" if it resolves and exits 0). Detection never panics and never
// performs paid work.
func (a *Adapter) Detect(ctx context.Context) protocol.DetectionResult {
	bin, err := a.binary()
	if err != nil {
		return protocol.DetectionResult{
			Installed: false,
			Detail:    "claude executable not found on PATH: " + err.Error(),
		}
	}
	ver, _, exitCode, perr := a.runProbe(ctx, bin, []string{"--version"})
	if perr != nil {
		// Resolved but could not be launched (e.g. permissions). Treat as not
		// usable rather than guessing.
		return protocol.DetectionResult{
			Installed: false,
			Path:      bin,
			Detail:    "claude resolved but probe failed: " + perr.Error(),
		}
	}
	if exitCode != 0 {
		return protocol.DetectionResult{
			Installed: false,
			Path:      bin,
			Detail:    "claude --version exited " + strconv.Itoa(exitCode),
		}
	}
	detail := strings.TrimSpace(string(ver))
	pv, _ := parseVersion(detail)
	return protocol.DetectionResult{
		Installed: true,
		Path:      bin,
		Version:   pv.Full,
		Detail:    "claude detected" + versionSuffix(pv),
	}
}

func versionSuffix(pv parsedVersion) string {
	if pv.Full == "" {
		return ""
	}
	return " (" + pv.Full + ")"
}

// Version implements codingagent.Adapter. ProtocolVersion is always
// [protocol.ProtocolVersion] (1) for this adapter; the engine version is the
// detected Claude Code CLI semver (best-effort).
func (a *Adapter) Version(ctx context.Context) protocol.VersionResult {
	bin, err := a.binary()
	res := protocol.VersionResult{
		AdapterVersion:  a.opts.AdapterVersion,
		ProtocolVersion: protocol.ProtocolVersion,
	}
	if err != nil {
		res.Error = "claude not found: " + err.Error()
		return res
	}
	_, stderr, exitCode, perr := a.runProbe(ctx, bin, []string{"--version"})
	if perr != nil {
		res.Error = "version probe failed: " + perr.Error()
		return res
	}
	if exitCode != 0 {
		res.Error = "claude --version exited " + strconv.Itoa(exitCode) + ": " + strings.TrimSpace(string(stderr))
		return res
	}
	out, _, _, _ := a.runProbe(ctx, bin, []string{"--version"})
	pv, ok := parseVersion(string(out))
	if ok {
		res.EngineVersion = pv.Full
	}
	return res
}

// runProbe is a thin wrapper over the injected Probe seam that applies the
// probe timeout and baseline environment.
func (a *Adapter) runProbe(ctx context.Context, name string, args []string) (stdout, stderr []byte, exitCode int, err error) {
	pctx, cancel := context.WithTimeout(ctx, a.opts.ProbeTimeout)
	defer cancel()
	return a.opts.Probe(pctx, name, args, probeEnv())
}

// defaultProbe is the production [probeFunc]: it runs a short-lived command with
// the given environment, capturing stdout/stderr. err is non-nil only for
// launch/infrastructure failures; a non-zero exit is returned via exitCode.
func defaultProbe(ctx context.Context, name string, args []string, env []string) (stdout, stderr []byte, exitCode int, err error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	var out, eout bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &eout
	runErr := cmd.Run()
	exitCode = exitCodeFrom(runErr)
	if runErr != nil && exitCode == 0 {
		return out.Bytes(), eout.Bytes(), 0, runErr
	}
	return out.Bytes(), eout.Bytes(), exitCode, nil
}

// ---- PATHEXT-aware LookPath ----

// defaultLookPath resolves an executable using exec.LookPath first, then a
// manual PATH+PATHEXT fallback that also covers npm shims (.cmd/.bat on
// Windows, bare scripts on unix). It tolerates spaces and Unicode in PATH
// entries.
func defaultLookPath(file string) (string, error) {
	if p, err := exec.LookPath(file); err == nil {
		return p, nil
	}
	return searchPathExt(file)
}

// searchPathExt manually walks PATH, trying the bare name plus every PATHEXT
// extension (Windows) or the bare name only (unix). Returns the first existing
// candidate as an absolute path, or os.ErrNotExist if none match.
func searchPathExt(file string) (string, error) {
	// An absolute or relative path: resolve extensions against it directly.
	if filepath.IsAbs(file) || strings.ContainsRune(file, os.PathSeparator) || strings.ContainsRune(file, '/') {
		for _, p := range candidatePaths(file) {
			if isRegularFile(p) {
				return abs(p), nil
			}
		}
		return "", os.ErrNotExist
	}
	pathEnv := os.Getenv("PATH")
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			dir = "."
		}
		base := filepath.Join(dir, file)
		for _, p := range candidatePaths(base) {
			if isRegularFile(p) {
				return abs(p), nil
			}
		}
	}
	return "", os.ErrNotExist
}

// candidatePaths returns the names to probe for a base path: the base itself
// (covers unix shims and already-suffixed names) plus base+each PATHEXT ext
// (Windows). Comparisons against an existing extension are case-insensitive.
func candidatePaths(base string) []string {
	out := []string{base}
	for _, e := range pathExts() {
		if e == "" {
			continue
		}
		ext := e
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		if strings.EqualFold(filepath.Ext(base), ext) {
			continue
		}
		out = append(out, base+ext)
	}
	return out
}

// pathExts returns the PATHEXT extensions (Windows) or nil on non-Windows.
func pathExts() []string {
	ext := os.Getenv("PATHEXT")
	if ext == "" {
		return nil
	}
	// PATHEXT is a Windows environment variable whose value is always
	// semicolon-delimited, independent of the host OS path-list separator
	// (os.PathListSeparator is ':' on Unix). Split on ';' so a PATHEXT set on a
	// non-Windows host (e.g. in tests or cross-platform tooling) is parsed
	// correctly rather than treated as one giant entry.
	parts := strings.Split(ext, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func isRegularFile(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return false
	}
	return true
}

func abs(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// errNotInstalled is returned by binary() callers that want a sentinel.
var errNotInstalled = errors.New("claude: engine not installed")
