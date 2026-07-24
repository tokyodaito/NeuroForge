package declarative

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"neuroforge/internal/adapter/codingagent"
	"neuroforge/internal/adapter/codingagent/proctree"
	"neuroforge/internal/adapter/codingagent/protocol"
)

// templateVars are the "{{ placeholder }}" substitutions supported in a run
// command (spec §13.1): workspace, model, prompt_file, run_id, engine.
type templateVars struct {
	workspace  string
	model      string
	promptFile string
	runID      string
	engine     string
}

// Adapter is the declarative command coding-agent adapter (spec §13.1). A new
// CLI engine is registered from a [Manifest] with no Go code changes (AC-6).
type Adapter struct {
	manifest Manifest
	caps     protocol.AgentCapabilities

	mu     sync.Mutex
	runs   map[string]*runState
	artDir string // artifacts dir for malformed-output capture
}

// runState tracks one live declarative run for cancellation.
type runState struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
}

// New builds a declarative adapter from a parsed manifest. artifactsDir, when
// non-empty, is where malformed agent output lines are saved (spec: malformed
// event is saved to artifacts and classified); defaults to the system temp dir.
func New(m Manifest, artifactsDir string) *Adapter {
	a := &Adapter{
		manifest: m,
		runs:     map[string]*runState{},
		artDir:   artifactsDir,
	}
	a.caps = capsFromYAML(m.Capabilities)
	return a
}

// FromYAML parses a manifest and returns an adapter (convenience).
func FromYAML(data []byte, artifactsDir string) (*Adapter, error) {
	m, err := ParseManifest(data)
	if err != nil {
		return nil, err
	}
	return New(m, artifactsDir), nil
}

func capsFromYAML(c CapabilitiesYAML) protocol.AgentCapabilities {
	return protocol.AgentCapabilities{
		InteractiveMode:      c.InteractiveMode,
		HeadlessMode:         c.HeadlessMode,
		StreamingEvents:      c.StreamingEvents,
		StructuredOutput:     c.StructuredOutput,
		ImageInput:           c.ImageInput,
		SessionResume:        c.SessionResume,
		LiveUserMessages:     c.LiveUserMessages,
		ModelSelection:       c.ModelSelection,
		UsageReporting:       c.UsageReporting,
		CachedUsageReporting: c.CachedUsageReporting,
		ToolPermissions:      c.ToolPermissions,
		NativeSandbox:        c.NativeSandbox,
		MCP:                  c.MCP,
		ACP:                  c.ACP,
	}
}

// ID implements codingagent.Adapter.
func (a *Adapter) ID() string { return a.manifest.ID }

// Detect implements codingagent.Adapter. It runs the manifest's detect command
// (if any) and treats a zero exit as "installed".
func (a *Adapter) Detect(ctx context.Context) protocol.DetectionResult {
	cmd := a.manifest.Detect.Command
	if len(cmd) == 0 {
		return protocol.DetectionResult{Installed: true, Detail: "no detect command; assumed installed"}
	}
	resolved, err := exec.LookPath(cmd[0])
	if err != nil {
		return protocol.DetectionResult{Installed: false, Detail: fmt.Sprintf("not found: %s", cmd[0])}
	}
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	out, err := c.CombinedOutput()
	if err != nil {
		return protocol.DetectionResult{Installed: false, Path: resolved, Detail: fmt.Sprintf("detect failed: %v", err)}
	}
	return protocol.DetectionResult{Installed: true, Path: resolved, Version: strings.TrimSpace(string(out)), Detail: "detect ok"}
}

// Version implements codingagent.Adapter.
func (a *Adapter) Version(context.Context) protocol.VersionResult {
	return protocol.VersionResult{AdapterVersion: "declarative-v1", EngineVersion: a.manifest.ID, ProtocolVersion: protocol.ProtocolVersion}
}

// Health implements codingagent.Adapter.
func (a *Adapter) Health(ctx context.Context, _ protocol.Account) protocol.HealthResult {
	d := a.Detect(ctx)
	if d.Installed {
		return protocol.HealthResult{Status: protocol.HealthOK, Detail: d.Detail}
	}
	return protocol.HealthResult{Status: protocol.HealthDown, Detail: d.Detail}
}

// Capabilities implements codingagent.Adapter.
func (a *Adapter) Capabilities(context.Context) protocol.AgentCapabilities { return a.caps }

// ListModels implements codingagent.Adapter. A declarative adapter does not
// know provider models; it reports a single opaque model id (the manifest id).
func (a *Adapter) ListModels(context.Context, protocol.Account) ([]protocol.ModelDescriptor, error) {
	return []protocol.ModelDescriptor{{ID: a.manifest.ID + "/default", Engine: a.manifest.ID, Kind: protocol.ModelKindCoding}}, nil
}

// InspectQuota implements codingagent.Adapter. Declarative adapters have no
// quota signal, so they report UNKNOWN (spec §20.1, rule §36.10).
func (a *Adapter) InspectQuota(context.Context, protocol.Account) protocol.QuotaSnapshot {
	return protocol.QuotaSnapshot{Confidence: protocol.QuotaConfUnknown, State: protocol.QuotaStateUnknown, Reason: "declarative adapters report no quota"}
}

// Start implements codingagent.Adapter. It substitutes the run command template,
// spawns the agent in a new process group, and streams JSONL normalized events
// to sink. Malformed lines are saved to the artifacts dir and surfaced as
// warning events (spec: malformed event is saved + classified, not fatal).
func (a *Adapter) Start(ctx context.Context, req protocol.AgentRunRequest, sink codingagent.EventSink) (protocol.RunHandle, error) {
	return a.run(ctx, req, sink, false)
}

// Resume implements codingagent.Adapter.
func (a *Adapter) Resume(ctx context.Context, req protocol.ResumeRequest, sink codingagent.EventSink) (protocol.RunHandle, error) {
	return a.run(ctx, protocol.AgentRunRequest{
		RunID: req.RunID, Engine: req.Engine, Model: req.Model, Account: req.Account,
		Workspace: req.Workspace, Scope: req.Scope, AllowlistEnv: req.AllowlistEnv,
		TurnLimit: req.TurnLimit, Timeout: req.Timeout, SessionID: req.SessionID,
	}, sink, true)
}

func (a *Adapter) run(ctx context.Context, req protocol.AgentRunRequest, sink codingagent.EventSink, isResume bool) (protocol.RunHandle, error) {
	cmdLine := a.manifest.Run.Command
	if len(cmdLine) == 0 {
		return protocol.RunHandle{}, errors.New("declarative: manifest has no run.command")
	}
	tv := templateVars{
		workspace:  req.Workspace,
		model:      req.Model,
		promptFile: req.PromptFile,
		runID:      req.RunID,
		engine:     a.manifest.ID,
	}
	argv := substituteAll(cmdLine, tv)

	runCtx, cancel := context.WithCancel(ctx)
	cmd := proctree.NewGroupCommand(argv[0], argv[1:]...)
	cmd.Dir = req.Workspace
	cmd.Env = buildEnv(req.AllowlistEnv)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return protocol.RunHandle{}, fmt.Errorf("declarative: stdout pipe: %w", err)
	}
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Start(); err != nil {
		cancel()
		return protocol.RunHandle{}, fmt.Errorf("declarative: start agent: %w", err)
	}

	runID := req.RunID
	if runID == "" {
		runID = "declarative-run"
	}
	a.mu.Lock()
	a.runs[runID] = &runState{cmd: cmd, cancel: cancel}
	a.mu.Unlock()

	handle := protocol.RunHandle{RunID: runID, Engine: a.manifest.ID, Model: req.Model, Account: req.Account, SessionID: req.SessionID}

	// If resuming and the agent's first emitted event is run.started, the
	// caller maps it; declarative adapters rely on the command's own output.
	_ = isResume

	go a.supervise(runCtx, runID, cmd, stdout, sink)
	return handle, nil
}

// supervise reads JSONL until EOF, parses each line, forwards events to sink,
// captures malformed lines as artifacts, and ensures a terminal event is emitted
// when the process exits without one. It is responsive to cancellation: the
// blocking pipe read happens in a goroutine so ctx cancellation preempts it and
// terminates the whole process group (spec: cancellation ends the process group).
func (a *Adapter) supervise(ctx context.Context, runID string, cmd *exec.Cmd, stdout interface{ Read([]byte) (int, error) }, sink codingagent.EventSink) {
	defer func() {
		a.mu.Lock()
		delete(a.runs, runID)
		a.mu.Unlock()
	}()

	sawTerminal := false
	scanner := newLineScanner(stdout)
	type readResult struct {
		line    []byte
		hasMore bool
	}
	ch := make(chan readResult, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			line, hasMore := scanner.Next()
			select {
			case ch <- readResult{line: line, hasMore: hasMore}:
			case <-ctx.Done():
				return
			}
			if !hasMore && line == nil {
				return
			}
		}
	}()

	readEOF := false
	for !readEOF && !sawTerminal {
		select {
		case <-ctx.Done():
			// Caller cancelled: kill the process group and emit a cancel event.
			_ = proctree.KillGroup(cmd, proctree.SigKill)
			<-readerDone
			if !sawTerminal {
				_ = sink.OnEvent(context.Background(), terminalCancel(runID, a.manifest.ID))
			}
			return
		case res := <-ch:
			if res.line == nil && !res.hasMore {
				readEOF = true
				continue
			}
			if res.line == nil {
				continue
			}
			ev, perr := protocol.ParseEventLine(res.line)
			if perr != nil {
				// Malformed/unknown line: save as artifact and emit a warning.
				a.saveMalformed(runID, res.line)
				if ev.Type != "" {
					_ = sink.OnEvent(ctx, ev)
				}
				continue
			}
			if ev.RunID == "" {
				ev.RunID = runID
			}
			if ev.Engine == "" {
				ev.Engine = a.manifest.ID
			}
			if ev.Type.IsTerminal() {
				sawTerminal = true
			}
			if err := sink.OnEvent(ctx, ev); err != nil {
				// Consumer aborted; kill the group and stop.
				_ = proctree.KillGroup(cmd, proctree.SigKill)
				<-readerDone
				return
			}
		}
	}

	// Process exited. Wait for it to collect the exit code / stderr.
	waitErr := cmd.Wait()
	stderr := ""
	if buf, ok := cmd.Stderr.(*bytes.Buffer); ok {
		stderr = buf.String()
	}
	exitCode := exitCodeFrom(waitErr)

	if !sawTerminal {
		// No terminal event from the agent → synthesize one from the outcome.
		fc := codingagent.DefaultClassify(exitCode, nil, stderr)
		term := protocol.EventRunCompleted
		if exitCode != 0 {
			term = protocol.EventRunFailed
		}
		ev := protocol.NormalizedEvent{
			Type: term, Timestamp: time.Now(), RunID: runID, Engine: a.manifest.ID,
		}
		if term == protocol.EventRunFailed {
			ev.Failure = &protocol.FailurePayload{Class: fc.Class, Reason: fc.Reason, ExitCode: exitCode}
		}
		_ = sink.OnEvent(context.Background(), ev)
	}
}

// saveMalformed persists a malformed agent output line to the artifacts dir so
// it is recoverable for forensics (spec: malformed event saved to artifacts).
func (a *Adapter) saveMalformed(runID string, line []byte) {
	dir := a.artDir
	if dir == "" {
		dir = os.TempDir()
	}
	name := fmt.Sprintf("malformed-%s-%d.txt", sanitize(runID), time.Now().UnixNano())
	_ = os.WriteFile(filepath.Join(dir, name), line, 0o600)
}

// SendMessage implements codingagent.Adapter. Declarative adapters have no live
// message channel unless their command reads from a known fd; not supported in
// v1 (capabilities default LiveUserMessages=false).
func (a *Adapter) SendMessage(context.Context, protocol.RunHandle, protocol.AgentMessage) error {
	return errors.New("declarative: live messages not supported in v1")
}

// Cancel implements codingagent.Adapter. It terminates the whole process group
// (spec: cancellation ends the whole process group).
func (a *Adapter) Cancel(_ context.Context, handle protocol.RunHandle) error {
	a.mu.Lock()
	st, ok := a.runs[handle.RunID]
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("declarative: unknown run %q", handle.RunID)
	}
	st.cancel()
	return proctree.KillGroup(st.cmd, proctree.SigKill)
}

// ClassifyFailure implements codingagent.Adapter.
func (a *Adapter) ClassifyFailure(exitCode int, events []protocol.NormalizedEvent, stderr string) protocol.FailureClassification {
	return codingagent.DefaultClassify(exitCode, events, stderr)
}

// substituteAll applies template substitution to every argv element.
func substituteAll(argv []string, tv templateVars) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = substitute(a, tv)
	}
	return out
}

func substitute(s string, tv templateVars) string {
	repl := func(p string) string {
		switch p {
		case "workspace":
			return tv.workspace
		case "model":
			return tv.model
		case "prompt_file":
			return tv.promptFile
		case "run_id":
			return tv.runID
		case "engine":
			return tv.engine
		default:
			return ""
		}
	}
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '{' && i+1 < len(s) && s[i+1] == '{' {
			end := strings.Index(s[i:], "}}")
			if end < 0 {
				break
			}
			key := strings.TrimSpace(s[i+2 : i+end])
			b.WriteString(repl(key))
			i += end + 2
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	if b.Len() == 0 {
		return s
	}
	return b.String()
}

// buildEnv constructs the allowlisted environment for the agent process (spec
// §29.2): only PATH/HOME/TERM plus the caller's allowlist are passed. No merge
// tokens or daemon auth token are ever included (AC-28).
func buildEnv(allowlist []string) []string {
	env := []string{}
	keep := map[string]struct{}{"PATH": {}, "HOME": {}, "USER": {}, "LANG": {}, "LC_ALL": {}, "TERM": {}}
	for k := range keep {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	for _, kv := range allowlist {
		// allowlist entries are "KEY" (copied from the current env) or "KEY=VAL".
		if idx := strings.IndexByte(kv, '='); idx >= 0 {
			env = append(env, kv)
			continue
		}
		if v, ok := os.LookupEnv(kv); ok {
			env = append(env, kv+"="+v)
		}
	}
	return env
}

func exitCodeFrom(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '/' || r == ' ' || r == ':' {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func terminalCancel(runID, engine string) protocol.NormalizedEvent {
	return protocol.NormalizedEvent{
		Type: protocol.EventRunCancelled, Timestamp: time.Now(), RunID: runID, Engine: engine,
		Failure: &protocol.FailurePayload{Class: protocol.FailureCancelled, Reason: "cancelled by caller"},
	}
}
