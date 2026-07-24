package workspace

import "fmt"

// Branch naming per spec §17.3 / ADR-0007.
//
//   attempt branch: forge/<task-id>/<work-package-id>/attempt-<n>
//   result branch:  forge/result/<task-id>

// AttemptBranch returns the branch name for a work package attempt (§17.3).
func AttemptBranch(taskID, workPackageID string, attempt int) string {
	return fmt.Sprintf("forge/%s/%s/attempt-%d",
		sanitizeBranchSegment(taskID),
		sanitizeBranchSegment(workPackageID),
		attempt)
}

// ResultBranch returns the final local result branch name (§17.3).
func ResultBranch(taskID string) string {
	return "forge/result/" + sanitizeBranchSegment(taskID)
}

// WorkspaceID returns a deterministic id for a workspace given its components.
func WorkspaceID(taskID, workPackageID string, attempt int) string {
	return fmt.Sprintf("ws-%s-%s-%d",
		sanitizeBranchSegment(taskID),
		sanitizeBranchSegment(workPackageID),
		attempt)
}

// WorktreePath builds the worktree filesystem path under the managed
// workspaces root (§17.2):
//
//	<root>/workspaces/<project>/<task>/<work-package>/attempt-<n>
func WorktreePath(root, projectID, taskID, workPackageID string, attempt int) string {
	return fmt.Sprintf("%s/workspaces/%s/%s/%s/attempt-%d",
		root,
		sanitizeBranchSegment(projectID),
		sanitizeBranchSegment(taskID),
		sanitizeBranchSegment(workPackageID),
		attempt)
}
