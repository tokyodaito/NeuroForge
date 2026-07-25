package repoinfo

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// detectCommands inspects well-known manifest files and CI configs to suggest
// build/test/lint commands (§22.2 build/test graph). Found commands are
// deterministic and only ever SUGGESTED — the project onboarding (§8.3) is what
// confirms them with the user before persisting.
func detectCommands(idx *Index) {
	root := idx.Root
	if fileExists(filepath.Join(root, "go.mod")) {
		idx.BuildCmds = append(idx.BuildCmds, "go build ./...")
		idx.TestCmds = append(idx.TestCmds, "go test ./...")
		if fileExists(filepath.Join(root, "Makefile")) {
			idx.LintCmds = append(idx.LintCmds, "make lint")
		}
	}
	if fileExists(filepath.Join(root, "package.json")) {
		idx.BuildCmds = append(idx.BuildCmds, "npm run build")
		idx.TestCmds = append(idx.TestCmds, "npm test")
		idx.LintCmds = append(idx.LintCmds, "npm run lint")
	}
	if fileExists(filepath.Join(root, "Cargo.toml")) {
		idx.BuildCmds = append(idx.BuildCmds, "cargo build")
		idx.TestCmds = append(idx.TestCmds, "cargo test")
		idx.LintCmds = append(idx.LintCmds, "cargo clippy")
	}
	if fileExists(filepath.Join(root, "build.gradle")) || fileExists(filepath.Join(root, "build.gradle.kts")) {
		idx.BuildCmds = append(idx.BuildCmds, "./gradlew build")
		idx.TestCmds = append(idx.TestCmds, "./gradlew test")
	}
	if fileExists(filepath.Join(root, "pom.xml")) {
		idx.BuildCmds = append(idx.BuildCmds, "mvn package")
		idx.TestCmds = append(idx.TestCmds, "mvn test")
	}
	if fileExists(filepath.Join(root, "pyproject.toml")) || fileExists(filepath.Join(root, "setup.py")) {
		idx.TestCmds = append(idx.TestCmds, "pytest")
	}
	idx.BuildCmds = dedup(idx.BuildCmds)
	idx.TestCmds = dedup(idx.TestCmds)
	idx.LintCmds = dedup(idx.LintCmds)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func dedup(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// SearchMatch is one ranked result of a free-text search over the repo index
// (§22.2 SQLite-FTS analogue). Pure in-memory: the index is small enough that a
// ranked substring/term scan is deterministic and fast for v1.
type SearchMatch struct {
	Path    string
	Score   int
	Reasons []string
}

// Search ranks files by how well they match the query terms. Matching is on the
// path, symbol names and imports. Results are deterministic.
func (idx *Index) Search(query string, limit int) []SearchMatch {
	terms := splitTerms(query)
	if len(terms) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = 25
	}
	var matches []SearchMatch
	for i := range idx.Files {
		fe := &idx.Files[i]
		score, reasons := scoreFile(fe, terms)
		if score > 0 {
			matches = append(matches, SearchMatch{Path: fe.Path, Score: score, Reasons: reasons})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].Path < matches[j].Path
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

func splitTerms(query string) []string {
	query = strings.ToLower(query)
	fields := strings.Fields(query)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.Trim(f, "\"'.,;:()[]{}")
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func scoreFile(fe *FileEntry, terms []string) (int, []string) {
	score := 0
	var reasons []string
	lpath := strings.ToLower(fe.Path)
	for _, term := range terms {
		if strings.Contains(lpath, term) {
			score += 5
			reasons = append(reasons, "path:"+term)
		}
		for _, sym := range fe.Symbols {
			if strings.Contains(strings.ToLower(sym.Name), term) {
				score += 3
				reasons = append(reasons, "symbol:"+sym.Name)
				break
			}
		}
		for _, imp := range fe.Imports {
			if strings.Contains(strings.ToLower(imp), term) {
				score += 1
				reasons = append(reasons, "import:"+imp)
				break
			}
		}
	}
	return score, reasons
}

// RelatedChanges returns files historically changed alongside the given files
// (§22.2 "history of related changes"). This in-memory implementation ranks by
// shared directory and shared imports — a deterministic proxy for the git-log
// co-change graph that the daemon can enrich later.
func (idx *Index) RelatedChanges(paths []string, limit int) []SearchMatch {
	if len(paths) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = 15
	}
	want := map[string]bool{}
	wantImports := map[string]bool{}
	for _, p := range paths {
		want[p] = true
		if fe := idx.ByPath[p]; fe != nil {
			for _, imp := range fe.Imports {
				wantImports[imp] = true
			}
		}
	}
	var matches []SearchMatch
	for i := range idx.Files {
		fe := &idx.Files[i]
		if want[fe.Path] {
			continue
		}
		score := 0
		var reasons []string
		// Same package directory.
		for _, p := range paths {
			if dirOf(fe.Path) == dirOf(p) {
				score += 4
				reasons = append(reasons, "same-dir:"+dirOf(fe.Path))
				break
			}
		}
		// Shared imports.
		for _, imp := range fe.Imports {
			if wantImports[imp] {
				score += 2
				reasons = append(reasons, "shared-import:"+imp)
				break
			}
		}
		if score > 0 {
			matches = append(matches, SearchMatch{Path: fe.Path, Score: score, Reasons: reasons})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].Path < matches[j].Path
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

func dirOf(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i]
	}
	return ""
}
