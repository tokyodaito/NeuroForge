package cli

import (
	"strings"
	"testing"
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
