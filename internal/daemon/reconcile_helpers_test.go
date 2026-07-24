package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"neuroforge/internal/storage"
)

func runGitInDaemonTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = os.MkdirAll(dir, 0o755)
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func mustWriteDaemonTest(t *testing.T, path, content string) {
	t.Helper()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustExecDaemonTest(t *testing.T, db *storage.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}
