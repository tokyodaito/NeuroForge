package policy

import (
	"path/filepath"
	"strings"
)

// TestPathConvention describes how test files are recognised for a project's
// language/ecosystem (spec §24.2). The scope validator uses this to decide
// whether a changed path is a test file: when test generation is disabled,
// writing to any test path is a SCOPE_VIOLATION.
type TestPathConvention struct {
	// Suffixes are file suffixes that identify a test file (e.g. "_test.go").
	Suffixes []string
	// Substrings are path substrings that identify a test path (e.g. "/test/",
	// "/__tests__/"). Matched case-insensitively on the slash-normalised path.
	Substrings []string
}

// DefaultTestConventions covers the common ecosystems. The list is deliberately
// conservative: a path is a test path only if it clearly looks like one.
var DefaultTestConventions = []TestPathConvention{
	{Suffixes: []string{"_test.go"}},                                 // Go
	{Suffixes: []string{".test.js", ".test.ts", ".test.tsx"}},        // JS/TS jest
	{Suffixes: []string{".spec.js", ".spec.ts", ".spec.tsx"}},        // JS/TS spec
	{Substrings: []string{"/test/", "/tests/", "/__tests__/"}},       // generic dirs
	{Substrings: []string{"/src/test/"}},                             // Maven/Gradle
	{Suffixes: []string{"test.py", "_test.py"}},                      // Python
	{Suffixes: []string{"Test.java", "Tests.java", "IT.java"}},       // Java JUnit
	{Suffixes: []string{"_spec.rb"}, Substrings: []string{"/spec/"}}, // Ruby RSpec
}

// IsTestPath reports whether path looks like a test file under any of the given
// conventions. When no conventions are supplied, DefaultTestConventions is used.
// Path separators are normalised so the check works cross-platform.
func IsTestPath(path string, convs ...TestPathConvention) bool {
	if len(convs) == 0 {
		convs = DefaultTestConventions
	}
	norm := filepath.ToSlash(path)
	// Ensure the path has a leading slash for substring matching so directory
	// segments like "/test/" match even for top-level paths.
	matchPath := norm
	if !strings.HasPrefix(matchPath, "/") {
		matchPath = "/" + matchPath
	}
	lower := strings.ToLower(matchPath)
	for _, c := range convs {
		for _, sfx := range c.Suffixes {
			if strings.HasSuffix(lower, strings.ToLower(sfx)) {
				return true
			}
		}
		for _, sub := range c.Substrings {
			if strings.Contains(lower, strings.ToLower(sub)) {
				return true
			}
		}
	}
	return false
}

// ScopeCheckResult is the outcome of validating a file change against the test
// scope rule (§24.2).
type ScopeCheckResult struct {
	Path       string
	IsTest     bool
	Allowed    bool
	DenialCode string // "test_generation_disabled" | ""
	Reason     string
}

// CheckTestScope validates a single file path against the §24.2 rule: when test
// generation is disabled (Generate=false), test paths are forbidden. The
// ModifyExisting toggle does NOT override this — when Generate is off, all test
// file changes are rejected by normalisation (R6) and by this check.
//
// When Generate is true, test paths are allowed subject to ModifyExisting
// (modifying an existing test file requires ModifyExisting=true; creating a new
// one requires Generate=true).
func CheckTestScope(p Pipeline, path string, isNew bool, convs ...TestPathConvention) ScopeCheckResult {
	isTest := IsTestPath(path, convs...)
	if !isTest {
		return ScopeCheckResult{Path: path, IsTest: false, Allowed: true}
	}
	// Test path. If generation is off, ALL test changes are forbidden (§24.2).
	if !p.Tests.Generate {
		return ScopeCheckResult{
			Path:       path,
			IsTest:     true,
			Allowed:    false,
			DenialCode: "test_generation_disabled",
			Reason:     "tests.generate is disabled: creating or modifying test files is forbidden (§24.2)",
		}
	}
	// Generation on. A new test file is allowed (Generate=true). Modifying an
	// existing test file requires ModifyExisting (normalised to false when
	// Generate is off, so this branch only runs when Generate is on).
	if !isNew && !p.Tests.ModifyExisting {
		return ScopeCheckResult{
			Path:       path,
			IsTest:     true,
			Allowed:    false,
			DenialCode: "modify_existing_disabled",
			Reason:     "tests.modify_existing is disabled: modifying existing test files is forbidden",
		}
	}
	return ScopeCheckResult{Path: path, IsTest: true, Allowed: true}
}

// CheckFileChanges validates a batch of file changes against the test scope rule.
// Returns the first denial (if any) and the count of denied paths.
func CheckFileChanges(p Pipeline, changes []FileChange, convs ...TestPathConvention) (ScopeCheckResult, int) {
	denied := 0
	var first ScopeCheckResult
	for _, c := range changes {
		r := CheckTestScope(p, c.Path, c.IsNew, convs...)
		if r.IsTest && !r.Allowed {
			denied++
			if first.Path == "" {
				first = r
			}
		}
	}
	return first, denied
}

// FileChange describes one file mutation for scope validation.
type FileChange struct {
	Path  string
	IsNew bool // true = created, false = modified
}
