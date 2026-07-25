package repoinfo_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"neuroforge/internal/repoinfo"
)

// writeRepo creates a small fixture repository on disk and returns its root.
func writeRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, root, "go.mod", "module demo\n\ngo 1.23\n")
	mustWrite(t, root, "main.go", "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n")
	mustWrite(t, root, "user/user.go", "package user\n\ntype User struct {\n\tName string\n}\n\nfunc (u *User) Greet() string {\n\treturn u.Name\n}\n")
	mustWrite(t, root, "user/user_test.go", "package user\n\nimport \"testing\"\n\nfunc TestGreet(t *testing.T) {}\n")
	mustWrite(t, root, "internal/server/server.go", "package server\n\nimport (\n\t\"net/http\"\n)\n\nfunc Handler() {}\n")
	mustWrite(t, root, "README.md", "# Demo\n")
	mustWrite(t, root, "vendor/external/dep.go", "package external\n")
	mustWrite(t, root, "node_modules/pkg/index.js", "module.exports = 1\n")
	mustWrite(t, root, "Makefile", "check:\n\tgo test ./...\n")
	return root
}

func mustWrite(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildIndex(t *testing.T) {
	root := writeRepo(t)
	idx, err := repoinfo.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	if idx.ByPath["main.go"] == nil {
		t.Fatalf("main.go not indexed; have %d files", len(idx.Files))
	}
	fe := idx.ByPath["main.go"]
	if fe.Language != "go" {
		t.Errorf("main.go language = %q want go", fe.Language)
	}
	if len(fe.Symbols) == 0 {
		t.Errorf("main.go has no symbols")
	}
	// node_modules is skipped from descent (§22.2); vendor is indexed but
	// without symbols (cheap map entry only).
	if idx.ByPath["node_modules/pkg/index.js"] != nil {
		t.Errorf("node_modules should be skipped from index descent")
	}
	if idx.ByPath["vendor/external/dep.go"] == nil {
		t.Errorf("vendor file should be indexed as a map entry")
	} else if len(idx.ByPath["vendor/external/dep.go"].Symbols) != 0 {
		t.Errorf("vendor symbols should not be indexed (kept cheap)")
	}
	if !idx.ByPath["vendor/external/dep.go"].IsVendor {
		t.Errorf("vendor path not flagged as vendor")
	}
	// Build/test commands detected.
	if len(idx.BuildCmds) == 0 || !contains(idx.BuildCmds, "go build ./...") {
		t.Errorf("build commands = %v, want go build ./...", idx.BuildCmds)
	}
	if !contains(idx.TestCmds, "go test ./...") {
		t.Errorf("test commands = %v", idx.TestCmds)
	}
	// Test file recognised.
	if !idx.ByPath["user/user_test.go"].IsTest {
		t.Errorf("user_test.go not recognised as test")
	}
}

func contains(slice []string, s string) bool {
	for _, x := range slice {
		if x == s {
			return true
		}
	}
	return false
}

func TestSearchRanksBySymbolAndPath(t *testing.T) {
	root := writeRepo(t)
	idx, err := repoinfo.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	matches := idx.Search("User", 10)
	if len(matches) == 0 {
		t.Fatalf("no matches for User")
	}
	if matches[0].Path != "user/user.go" {
		t.Errorf("top match = %q want user/user.go", matches[0].Path)
	}
}

func TestRelatedChangesSharedImport(t *testing.T) {
	root := writeRepo(t)
	idx, err := repoinfo.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	// user/user.go and internal/server/server.go share no import here; but
	// related by directory with itself returns nothing. Verify RelatedChanges
	// does not return the seed path and ranks same-dir files.
	rel := idx.RelatedChanges([]string{"user/user.go"}, 10)
	for _, m := range rel {
		if m.Path == "user/user.go" {
			t.Errorf("seed path returned in related changes")
		}
	}
}

func TestAssemblePackRespectsBudget(t *testing.T) {
	root := writeRepo(t)
	idx, err := repoinfo.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	pack, err := idx.AssemblePack(repoinfo.PackOptions{
		Specification: "Implement a user greeting feature.",
		AllowedScope:  []string{"user/user.go"},
		QueryTerms:    []string{"user"},
		Commands:      idx.BuildCmds,
		Budget:        800,
		MaxFiles:      5,
		ExcerptLines:  60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pack.EstimatedTokens > 800+50 { // small slack for the estimator rounding
		t.Errorf("pack tokens %d exceeded budget 800 (§22.1)", pack.EstimatedTokens)
	}
	if len(pack.RelevantFiles) == 0 {
		t.Errorf("pack has no relevant files")
	}
	// The scope file must be present.
	found := false
	for _, f := range pack.RelevantFiles {
		if f.Path == "user/user.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("allowed-scope file user/user.go not included")
	}
	if pack.RepoMap == "" {
		t.Errorf("repo map should be included when budget allows")
	}
}

func TestAssemblePackNeverWholeRepo(t *testing.T) {
	root := writeRepo(t)
	idx, err := repoinfo.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	pack, _ := idx.AssemblePack(repoinfo.PackOptions{
		Specification: "x",
		Budget:        300,
	})
	// Even a tiny pack must not dump every file's full body. Total tokens are
	// bounded; the whole-repo body would exceed 300.
	if pack.EstimatedTokens > 300+50 {
		t.Errorf("token budget not enforced: %d", pack.EstimatedTokens)
	}
}

func TestEstimateTokensStable(t *testing.T) {
	a := repoinfo.EstimateTokens("hello world foo bar baz")
	b := repoinfo.EstimateTokens("hello world foo bar baz")
	if a != b || a == 0 {
		t.Errorf("token estimate not stable/zero: %d vs %d", a, b)
	}
}

func TestSliceLogKeepsFirstErrorAndStackTrace(t *testing.T) {
	raw := `building...
compiling main
main.go:12: cannot use "x" (string) as int
	main.go:12: some detail
	main.go:14: more detail
goroutine 1 [running]:
something else
PASS
`
	s := repoinfo.SliceLog(raw, 1, "go build ./...", "/logs/build.log")
	if s.FirstError == "" {
		t.Fatalf("first error empty")
	}
	if !strings.Contains(s.FirstError, "cannot use") {
		t.Errorf("first error = %q", s.FirstError)
	}
	if len(s.StackTrace) == 0 {
		t.Errorf("no stack trace captured")
	}
	if s.FullLogLink != "/logs/build.log" {
		t.Errorf("full log link lost")
	}
	if s.EstimatedTokens > repoinfo.MaxLogTokens {
		t.Errorf("log slice exceeds token budget: %d", s.EstimatedTokens)
	}
}

func TestAssembleDeltaNoFullHistory(t *testing.T) {
	root := writeRepo(t)
	idx, err := repoinfo.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	d, err := idx.AssembleDelta(repoinfo.DeltaOptions{
		Finding:         "Greet returns empty for unnamed users",
		Severity:        "major",
		Diff:            "+ return \"\"",
		FailingTest:     "TestGreet",
		FailingTestLog:  "exit: 1\nfirst error: TestGreet failed\n",
		ImplicatedPaths: []string{"user/user.go"},
		NextObjective:   "Return a default greeting.",
		Budget:          2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	rendered := d.Render()
	if !strings.Contains(rendered, "## Finding") {
		t.Errorf("delta missing finding section")
	}
	if strings.Contains(rendered, "research history") {
		t.Errorf("delta must NOT include full research history (§22.5)")
	}
	if d.EstimatedTokens > 2000+50 {
		t.Errorf("delta exceeds budget: %d", d.EstimatedTokens)
	}
}

func TestFingerprintStable(t *testing.T) {
	fp1 := repoinfo.FingerprintPrompt([]string{"b", "a", "c"})
	fp2 := repoinfo.FingerprintPrompt([]string{"c", "a", "b"}) // same set, diff order
	if fp1.Hash != fp2.Hash {
		t.Errorf("fingerprint not order-stable: %s vs %s", fp1.Hash, fp2.Hash)
	}
	if !repoinfo.IsCacheHit(fp1, fp2) {
		t.Errorf("cache hit not detected for stable prefix")
	}
	fp3 := repoinfo.FingerprintPrompt([]string{"a", "b", "c", "d"})
	if repoinfo.IsCacheHit(fp1, fp3) {
		t.Errorf("different content reported as cache hit")
	}
}

func TestStablePrefixDeterministic(t *testing.T) {
	a := repoinfo.StablePrefix([]string{"z", "a"}, []string{"cmd2", "cmd1"}, "MAP")
	b := repoinfo.StablePrefix([]string{"a", "z"}, []string{"cmd1", "cmd2"}, "MAP")
	if a != b {
		t.Errorf("stable prefix not deterministic")
	}
}
