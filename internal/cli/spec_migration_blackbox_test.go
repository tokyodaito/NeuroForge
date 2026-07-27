package cli

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"neuroforge/internal/daemon"
	"neuroforge/internal/storage"
)

// TestSpecMigration_BlackBox_DaemonAppliesV8 proves the compiled task
// specification schema (migration v8) is applied by the PRODUCTION daemon path
// and is observable through the compiled `forge` binary — not via internal Go
// objects (engineering baseline §2: blackbox evidence).
//
// Scenario:
//  1. `forge daemon start` against a fresh home → daemon.Run calls db.Migrate,
//     which must apply migration v8 (the task_specifications,
//     task_acceptance_criteria and task_specification_sequences tables).
//  2. `forge doctor` reports the now-active schema version (the database check
//     reads storage.CurrentVersion).
//  3. The schema is verified to contain the new tables by querying the home's
//     DB file through the real SQLite driver (read-only).
//  4. `forge daemon stop` then `forge daemon start` again → idempotent restart
//     recovery (re-migrate is a no-op, schema version unchanged, no error).
//
// Spec CRUD itself (save/get/lock) has no CLI/transport surface in M14-01 (the
// compiler lands in a later milestone); it is proven by real-SQLite integration
// tests in internal/task and internal/storage (not fake-only: real driver, real
// migrations, real DB file, restart recovery).
func TestSpecMigration_BlackBox_DaemonAppliesV8(t *testing.T) {
	if testing.Short() {
		t.Skip("black-box daemon lifecycle test spawns the real daemon")
	}
	bin := forgeBinary(t)
	home := t.TempDir()
	withDaemonCleanup(t, bin, home)

	// 1. Start the daemon (production path: daemon.Run → db.Migrate).
	out, _, code := runForge(t, bin, home, "daemon", "start")
	if code != 0 {
		t.Fatalf("daemon start: exit %d out=%s", code, out)
	}
	if !strings.Contains(out, "started") {
		t.Fatalf("daemon start output: %q", out)
	}

	// Wait for readiness (the start command already waits, but belt-and-braces).
	if !waitForDaemonRunning(t, bin, home, 10*time.Second) {
		t.Fatalf("daemon did not reach running state")
	}

	// 2. doctor must report the active schema version (>= 8: v8 is the spec
	//    migration; the assertion is forward-compatible with later migrations).
	out, _, code = runForge(t, bin, home, "doctor")
	if code != 0 {
		t.Fatalf("doctor: exit %d out=%s", code, out)
	}
	version := parseSchemaVersion(t, out)
	if version < 8 {
		t.Fatalf("schema version %d < 8; migration v8 not applied by daemon (doctor output:\n%s)", version, out)
	}

	// 3. The new tables must physically exist in the home's DB (read through the
	//    real SQLite driver, not the binary — this is a structural check of the
	//    production DB file the daemon just migrated).
	dirs := daemon.WithRoot(home)
	if err := assertSpecTablesExist(dirs.StateDB); err != nil {
		t.Fatalf("spec tables missing from production DB: %v", err)
	}

	// 4. Idempotent restart recovery: stop, then start again. The re-Migrate on
	//    the second start must be a no-op (no error, schema unchanged).
	if out, _, code := runForge(t, bin, home, "daemon", "stop"); code != 0 {
		t.Fatalf("daemon stop: exit %d out=%s", code, out)
	}
	if out, _, code := runForge(t, bin, home, "daemon", "start"); code != 0 {
		t.Fatalf("daemon restart: exit %d out=%s", code, out)
	}
	if !waitForDaemonRunning(t, bin, home, 10*time.Second) {
		t.Fatalf("daemon did not reach running state after restart")
	}
	out, _, code = runForge(t, bin, home, "doctor")
	if code != 0 {
		t.Fatalf("doctor after restart: exit %d out=%s", code, out)
	}
	versionAfter := parseSchemaVersion(t, out)
	if versionAfter != version {
		t.Fatalf("schema version changed on restart: %d -> %d (migrate not idempotent)", version, versionAfter)
	}
	// Tables still present after restart.
	if err := assertSpecTablesExist(dirs.StateDB); err != nil {
		t.Fatalf("spec tables missing after restart: %v", err)
	}
}

func waitForDaemonRunning(t *testing.T, bin, home string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, _, code := runForge(t, bin, home, "daemon", "status", "--json")
		if code == 0 && strings.Contains(out, `"state":"running"`) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// parseSchemaVersion extracts the integer N from the "schema vN" token in the
// doctor output. It fails the test if the token is absent (doctor must report
// the schema version for a migrated database).
func parseSchemaVersion(t *testing.T, doctorOut string) int {
	t.Helper()
	idx := strings.Index(doctorOut, "schema v")
	if idx < 0 {
		t.Fatalf("doctor output has no \"schema v\" token:\n%s", doctorOut)
	}
	rest := doctorOut[idx+len("schema v"):]
	var num int
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if c < '0' || c > '9' {
			break
		}
		num = num*10 + int(c-'0')
	}
	if num == 0 {
		t.Fatalf("could not parse schema version from doctor output:\n%s", doctorOut)
	}
	return num
}

// assertSpecTablesExist opens the production DB file read-only through the real
// SQLite driver and confirms the migration-v8 tables are present.
func assertSpecTablesExist(dbPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := sql.Open(storage.Driver, dbPath+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	for _, table := range []string{"task_specifications", "task_acceptance_criteria", "task_specification_sequences"} {
		var name string
		err := db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("table " + table + " not present")
		}
		if err != nil {
			return err
		}
	}
	return nil
}
