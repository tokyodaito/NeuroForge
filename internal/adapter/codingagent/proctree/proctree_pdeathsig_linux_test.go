//go:build linux

package proctree_test

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent/proctree"
)

// TestNewGroupCommand_SetsPdeathsig asserts (linux) that a command built by
// [proctree.NewGroupCommand] carries Pdeathsig=SIGKILL so the child cannot
// outlive the daemon (security review H1: an orphaned agent process group must
// not keep writing the worktree while restart recovery re-drives the run).
func TestNewGroupCommand_SetsPdeathsig(t *testing.T) {
	cmd := proctree.NewGroupCommand("sleep", "60")
	attr := cmd.SysProcAttr
	if attr == nil {
		t.Fatal("SysProcAttr is nil")
	}
	if !attr.Setpgid {
		t.Error("Setpgid not set (process-group kill would not work)")
	}
	if attr.Pdeathsig != syscall.SIGKILL {
		t.Errorf("Pdeathsig = %v, want SIGKILL", attr.Pdeathsig)
	}
}

// TestHelperProcess is not a test; it is the child-worker entry point for
// TestPdeathsig_ChildDiesWithParent. In "spawner" mode it starts a sleeper via
// NewGroupCommand, prints the sleeper's pid and exits immediately — the
// sleeper must then die from Pdeathsig even though nobody killed it.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_PROCTREE_HELPER") != "spawner" {
		return
	}
	cmd := proctree.NewGroupCommand("sleep", "300")
	if err := cmd.Start(); err != nil {
		os.Exit(2)
	}
	// Report the pid on stdout, then exit WITHOUT waiting or killing: only
	// Pdeathsig can reap the sleeper now.
	os.Stdout.WriteString(strconv.Itoa(cmd.Process.Pid) + "\n")
	os.Exit(0)
}

// TestPdeathsig_ChildDiesWithParent proves the H1 containment property with
// real processes: a process group spawned via NewGroupCommand is SIGKILLed by
// the kernel when its parent process dies abruptly (no cleanup, no KillGroup).
func TestPdeathsig_ChildDiesWithParent(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real processes")
	}
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep binary not available")
	}

	helper := exec.Command(os.Args[0], "-test.run=TestHelperProcess")
	helper.Env = append(os.Environ(), "GO_PROCTREE_HELPER=spawner")
	out, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	buf := make([]byte, 64)
	n, err := out.Read(buf)
	if err != nil || n == 0 {
		t.Fatalf("read sleeper pid: n=%d err=%v", n, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if err != nil || pid <= 0 {
		t.Fatalf("parse sleeper pid %q: %v", string(buf[:n]), err)
	}
	// The helper now exits abruptly (it never waits on the sleeper).
	if err := helper.Wait(); err != nil {
		t.Fatalf("helper exit: %v", err)
	}

	if err := waitForDead(pid, 10*time.Second); err != nil {
		// Best-effort cleanup so a failed assertion never leaks a sleeper.
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("sleeper %d survived its parent's death (Pdeathsig broken): %v", pid, err)
	}
}
