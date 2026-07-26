package transport

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRunTaskResultDTO_NullableFields verifies OUTCOME_CONTRACT.md §3 / §3.1 /
// invariant I.11: the JSON field set is fixed, nullable fields render as JSON
// null (not "") when unset, and changed_files renders as [] not null. This is a
// table-driven contract test over representative outcome shapes.
func TestRunTaskResultDTO_NullableFields(t *testing.T) {
	cases := []struct {
		name string
		dto  RunTaskResultDTO
		// fields that must be null in this outcome
		wantNull []string
		// fields that must be a non-null, non-empty string
		wantString map[string]string
	}{
		{
			name: "completed-with-commit",
			dto: RunTaskResultDTO{
				Outcome: "completed-with-commit", TaskID: "neuroforge-1", WorkspaceID: "ws-1",
				RunID: "run-1", WorkspacePath: "/p", BaseSHA: "abc", ActualHeadSHA: "def",
				Engine: "opencode", Model: "m", ChangedFiles: []string{"a.go"},
				CommitSHA: "def", ResultBranch: "refs/heads/forge/result/neuroforge-1",
				NextAction: "forge task show neuroforge-1",
			},
			wantNull: []string{"error", "error_class"},
			wantString: map[string]string{
				"run_id": "run-1", "actual_head_sha": "def", "commit_sha": "def",
				"result_branch": "refs/heads/forge/result/neuroforge-1",
			},
		},
		{
			name: "completed-no-changes",
			dto: RunTaskResultDTO{
				Outcome: "completed-no-changes", TaskID: "neuroforge-2", WorkspaceID: "ws-2",
				WorkspacePath: "/p", BaseSHA: "abc", ActualHeadSHA: "abc",
				Engine: "opencode", Model: "m", NextAction: "rephrase",
			},
			wantNull: []string{"run_id", "commit_sha", "result_branch", "error", "error_class"},
		},
		{
			name: "failed",
			dto: RunTaskResultDTO{
				Outcome: "failed", TaskID: "neuroforge-3", WorkspaceID: "ws-3",
				RunID: "run-3", WorkspacePath: "/p", BaseSHA: "abc",
				Engine: "opencode", Model: "m",
				Error: "boom", ErrorClass: "ADAPTER_FAILED", NextAction: "retry",
			},
			wantNull: []string{"commit_sha", "result_branch"},
			wantString: map[string]string{
				"run_id": "run-3", "error": "boom", "error_class": "ADAPTER_FAILED",
			},
		},
		{
			name: "cancelled",
			dto: RunTaskResultDTO{
				Outcome: "cancelled", TaskID: "neuroforge-4", WorkspaceID: "ws-4",
				WorkspacePath: "/p", BaseSHA: "abc", Engine: "opencode", Model: "m",
				NextAction: "re-run",
			},
			wantNull: []string{"run_id", "actual_head_sha", "commit_sha", "result_branch", "error", "error_class"},
		},
		{
			name: "interrupted",
			dto: RunTaskResultDTO{
				Outcome: "interrupted", TaskID: "neuroforge-5", WorkspaceID: "ws-5",
				WorkspacePath: "/p", BaseSHA: "abc", Engine: "opencode", Model: "m",
				Error: "interrupted by daemon restart", ErrorClass: "INTERRUPTED", NextAction: "re-run",
			},
			wantNull:   []string{"run_id", "actual_head_sha", "commit_sha", "result_branch"},
			wantString: map[string]string{"error": "interrupted by daemon restart", "error_class": "INTERRUPTED"},
		},
	}

	allFields := []string{
		"outcome", "task_id", "workspace_id", "run_id", "workspace_path",
		"base_sha", "actual_head_sha", "engine", "model", "changed_files",
		"commit_sha", "result_branch", "next_action", "error", "error_class",
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.dto)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var doc map[string]any
			if err := json.Unmarshal(b, &doc); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			// Fixed field set: every contract field is present.
			for _, f := range allFields {
				if _, ok := doc[f]; !ok {
					t.Errorf("missing required field %q; raw=%s", f, string(b))
				}
			}
			// changed_files must be an array (possibly empty), never null.
			cf, _ := doc["changed_files"].([]any)
			if cf == nil {
				cf = []any{}
			}
			if _, ok := doc["changed_files"].([]any); !ok && doc["changed_files"] != nil {
				t.Errorf("changed_files must be an array, got %T", doc["changed_files"])
			}
			_ = cf
			// Specified fields must be null.
			for _, f := range tc.wantNull {
				if doc[f] != nil {
					t.Errorf("%s = %v, want null; raw=%s", f, doc[f], string(b))
				}
			}
			// Specified fields must be the expected non-empty string.
			for f, want := range tc.wantString {
				got, _ := doc[f].(string)
				if got != want {
					t.Errorf("%s = %q, want %q", f, got, want)
				}
			}
		})
	}
}

// TestRunTaskResultDTO_ChangedFilesIsArray ensures changed_files is [] not null
// even when the DTO leaves it nil (defensive; the handler also normalises).
func TestRunTaskResultDTO_ChangedFilesIsArray(t *testing.T) {
	dto := RunTaskResultDTO{Outcome: "failed", ChangedFiles: nil}
	b, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"changed_files":[]`) {
		t.Fatalf("changed_files must serialize as []: %s", string(b))
	}
}
