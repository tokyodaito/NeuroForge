package cli

import (
	"strings"
	"testing"
	"time"
)

// TestParseRunArgs_MaxRepairFlag (M7): `forge run` must accept --max-repair,
// default it to 3, and reject negative values before any daemon work.
func TestParseRunArgs_MaxRepairFlag(t *testing.T) {
	parsed, err := parseRunArgs([]string{"--engine", "fake", "fix", "the", "bug"})
	if err != nil {
		t.Fatalf("default parse: %v", err)
	}
	if parsed.MaxRepair != 3 {
		t.Errorf("default MaxRepair = %d, want 3", parsed.MaxRepair)
	}

	parsed, err = parseRunArgs([]string{"--engine", "fake", "--max-repair", "5", "fix the bug"})
	if err != nil {
		t.Fatalf("parse --max-repair 5: %v", err)
	}
	if parsed.MaxRepair != 5 {
		t.Errorf("MaxRepair = %d, want 5", parsed.MaxRepair)
	}

	if _, err := parseRunArgs([]string{"--engine", "fake", "--max-repair", "-1", "fix the bug"}); err == nil {
		t.Error("negative --max-repair must be rejected")
	} else if !strings.Contains(err.Error(), "max-repair") {
		t.Errorf("error should name the flag: %v", err)
	}
}

// TestParseRunArgs_WaitTimeoutFlag: `forge run` must accept --wait-timeout,
// default it to 2h (the previous hardcoded 30-minute request cap cancelled
// healthy durable runs mid-repair), and reject non-positive values.
func TestParseRunArgs_WaitTimeoutFlag(t *testing.T) {
	parsed, err := parseRunArgs([]string{"--engine", "fake", "fix", "the", "bug"})
	if err != nil {
		t.Fatalf("default parse: %v", err)
	}
	if parsed.WaitTimeout != 2*time.Hour {
		t.Errorf("default WaitTimeout = %s, want 2h", parsed.WaitTimeout)
	}

	parsed, err = parseRunArgs([]string{"--engine", "fake", "--wait-timeout", "45m", "fix the bug"})
	if err != nil {
		t.Fatalf("parse --wait-timeout 45m: %v", err)
	}
	if parsed.WaitTimeout != 45*time.Minute {
		t.Errorf("WaitTimeout = %s, want 45m", parsed.WaitTimeout)
	}

	if _, err := parseRunArgs([]string{"--engine", "fake", "--wait-timeout", "0", "fix the bug"}); err == nil {
		t.Error("zero --wait-timeout must be rejected")
	} else if !strings.Contains(err.Error(), "wait-timeout") {
		t.Errorf("error should name the flag: %v", err)
	}
	if _, err := parseRunArgs([]string{"--engine", "fake", "--wait-timeout", "-5m", "fix the bug"}); err == nil {
		t.Error("negative --wait-timeout must be rejected")
	}
}
