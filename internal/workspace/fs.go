package workspace

import (
	"os"
	"path/filepath"
)

// filepathWalk walks root looking for directories and calls fn for each. It
// returns all paths where fn returns true. If root does not exist, it returns
// an empty slice (no error).
func filepathWalk(root string, fn func(path string) bool) []string {
	var matches []string
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil
	}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if !d.IsDir() {
			return nil
		}
		if path == root {
			return nil
		}
		if fn(path) {
			matches = append(matches, path)
		}
		return nil
	})
	return matches
}

// isWorktree reports whether path looks like a git worktree (contains a .git
// file rather than a .git directory, which is the worktree signature).
func isWorktree(path string) bool {
	gitPath := filepath.Join(path, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return false
	}
	// A worktree has a .git *file* (pointing to the common dir); a primary
	// checkout has a .git *directory*.
	return !info.IsDir()
}

// WorktreeExists reports whether a managed worktree path still exists on disk.
// Used by the reconciler to detect stale workspaces after a crash or restart.
func WorktreeExists(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.IsDir(), nil
}
