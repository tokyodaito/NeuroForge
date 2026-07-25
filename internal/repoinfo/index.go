package repoinfo

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MaxRepoFiles caps how many files the index will walk (defence-in-depth so a
// huge monorepo cannot exhaust memory / the context budget — §22.1).
const MaxRepoFiles = 50000

// FileEntry describes one file in the indexed tree.
type FileEntry struct {
	Path        string // repo-relative, forward slashes
	Size        int64
	Lines       int
	Language    string
	IsTest      bool
	IsVendor    bool
	IsGenerated bool
	Symbols     []Symbol
	Imports     []string
}

// Symbol is a coarse, language-agnostic code symbol discovered by the index.
type Symbol struct {
	Name string
	Kind SymbolKind
	Line int
}

// SymbolKind is the coarse classification used by the symbol index.
type SymbolKind string

const (
	SymFunc      SymbolKind = "func"
	SymMethod    SymbolKind = "method"
	SymType      SymbolKind = "type"
	SymConst     SymbolKind = "const"
	SymVar       SymbolKind = "var"
	SymClass     SymbolKind = "class"
	SymInterface SymbolKind = "interface"
)

// Index is the assembled repository index (§22.2).
type Index struct {
	Root      string
	Files     []FileEntry
	ByPath    map[string]*FileEntry
	Languages map[string]int // language -> file count
	BuildCmds []string       // detected build commands
	TestCmds  []string       // detected test commands
	LintCmds  []string       // detected lint commands
}

// Build walks the worktree at root and builds the repository index. It is
// read-only and deterministic. VCS metadata (.git, .hg, .svn), vendored and
// generated trees are recorded but their symbols are not indexed. The walk is
// capped at MaxRepoFiles entries so a giant monorepo cannot exhaust memory
// (§22.1).
func Build(root string) (*Index, error) {
	idx := &Index{
		Root:      root,
		ByPath:    map[string]*FileEntry{},
		Languages: map[string]int{},
	}
	count := 0
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate unreadable subtrees
		}
		rel := relPath(root, path)
		if d.IsDir() {
			// Skip VCS metadata and heavy vendor trees from descent.
			if isSkipDir(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if count >= MaxRepoFiles {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		fe := fileEntry(root, rel, info.Size())
		idx.Files = append(idx.Files, fe)
		count++
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	// Re-point ByPath map and aggregate languages.
	for i := range idx.Files {
		fe := &idx.Files[i]
		idx.ByPath[fe.Path] = fe
		if fe.Language != "" {
			idx.Languages[fe.Language]++
		}
	}
	detectCommands(idx)
	sort.Slice(idx.Files, func(i, j int) bool { return idx.Files[i].Path < idx.Files[j].Path })
	return idx, nil
}

func relPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

func isSkipDir(rel string) bool {
	base := rel
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		base = rel[i+1:]
	}
	switch base {
	case ".git", ".hg", ".svn", "node_modules", ".next", ".cache", "dist", "build", "target":
		return true
	}
	return false
}

func fileEntry(root, rel string, size int64) FileEntry {
	fe := FileEntry{
		Path: rel,
		Size: size,
	}
	fe.Language = languageOf(rel)
	fe.IsTest = isTestPath(rel)
	fe.IsVendor = isVendorPath(rel)
	fe.IsGenerated = isGeneratedPath(rel)
	// Only index symbols/imports for smallish source files (keep it cheap and
	// deterministic; large files are represented as map entries only).
	if fe.Language != "" && !fe.IsVendor && !fe.IsGenerated && fe.Size < 256*1024 {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if f, err := os.Open(full); err == nil {
			fe.Symbols, fe.Imports, fe.Lines = scanSource(f, fe.Language)
			_ = f.Close()
		}
	}
	return fe
}

// languageOf maps a file extension to a coarse language label. Used for the
// language graph (§22.2) and test/build detection.
func languageOf(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return "go"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".jsx":
		return "javascript"
	case ".py":
		return "python"
	case ".java", ".kt", ".kts":
		return "jvm"
	case ".rs":
		return "rust"
	case ".rb":
		return "ruby"
	case ".cs":
		return "csharp"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".cxx", ".hpp":
		return "cpp"
	case ".swift":
		return "swift"
	case ".md":
		return "markdown"
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	}
	return ""
}

// isTestPath recognises test files across the common conventions (§24.2).
func isTestPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if strings.HasSuffix(base, "_test.go") {
		return true
	}
	if strings.HasSuffix(base, ".test.js") || strings.HasSuffix(base, ".spec.js") ||
		strings.HasSuffix(base, ".test.ts") || strings.HasSuffix(base, ".spec.ts") ||
		strings.HasSuffix(base, ".test.tsx") || strings.HasSuffix(base, ".spec.tsx") {
		return true
	}
	if strings.HasSuffix(base, "_test.py") || strings.HasPrefix(base, "test_") {
		return true
	}
	if strings.HasSuffix(base, "test.java") || strings.HasSuffix(base, "tests.java") {
		return true
	}
	if strings.HasSuffix(base, "_test.rb") || strings.HasPrefix(base, "test_") {
		return true
	}
	// Generic test/ or __tests__/ directories.
	if strings.Contains(path, "/test/") || strings.Contains(path, "/__tests__/") ||
		strings.Contains(path, "/src/test/") || strings.Contains(path, "/tests/") {
		return true
	}
	return false
}

func isVendorPath(path string) bool {
	return path == "vendor" || strings.HasPrefix(path, "vendor/") ||
		strings.Contains(path, "/vendor/") || strings.Contains(path, "/third_party/") ||
		strings.Contains(path, "/node_modules/")
}

func isGeneratedPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if strings.HasSuffix(base, ".pb.go") || strings.HasSuffix(base, ".pb.py") ||
		strings.HasSuffix(base, ".gen.go") || strings.HasSuffix(base, ".generated.go") {
		return true
	}
	return strings.HasSuffix(base, "generated.go")
}

// scanSource performs a single-pass, language-agnostic extraction of top-level
// symbols and import statements. It is deliberately conservative: it only
// recognises lines that begin a declaration so it never produces false symbols
// inside string literals. This is good enough for the repo map and stays cheap.
func scanSource(f *os.File, lang string) (symbols []Symbol, imports []string, lines int) {
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		raw := sc.Text()
		t := strings.TrimSpace(raw)
		if sym, ok := matchSymbol(lang, t, lineNo); ok && !seen[sym.Name+string(sym.Kind)] {
			symbols = append(symbols, sym)
			seen[sym.Name+string(sym.Kind)] = true
		}
		if imp := matchImport(lang, t); imp != "" {
			imports = append(imports, imp)
		}
	}
	lines = lineNo
	return symbols, imports, lines
}

// matchSymbol returns a symbol if the trimmed line begins a declaration for the
// given language. Conservative on purpose.
func matchSymbol(lang, t string, line int) (Symbol, bool) {
	switch lang {
	case "go":
		// "func Foo(", "func (r T) Method(", "type T struct", "type T interface", "var Name", "const Name"
		if strings.HasPrefix(t, "func ") {
			name := goDeclName(t[5:], '(')
			if name == "" {
				name = goDeclName(t[5:], ' ')
			}
			kind := SymFunc
			if strings.HasPrefix(t[5:], "(") {
				kind = SymMethod
			}
			if name != "" {
				return Symbol{Name: name, Kind: kind, Line: line}, true
			}
		}
		if strings.HasPrefix(t, "type ") {
			name := goDeclName(t[5:], ' ')
			kind := SymType
			if strings.Contains(t, "interface") {
				kind = SymInterface
			}
			if name != "" {
				return Symbol{Name: name, Kind: kind, Line: line}, true
			}
		}
		if strings.HasPrefix(t, "const ") {
			name := goDeclName(t[6:], ' ')
			if name != "" {
				return Symbol{Name: name, Kind: SymConst, Line: line}, true
			}
		}
		if strings.HasPrefix(t, "var ") {
			name := goDeclName(t[4:], ' ')
			if name != "" {
				return Symbol{Name: name, Kind: SymVar, Line: line}, true
			}
		}
	case "python", "ruby":
		if strings.HasPrefix(t, "def ") {
			name := pyDeclName(t[4:])
			if name != "" {
				return Symbol{Name: name, Kind: SymFunc, Line: line}, true
			}
		}
		if strings.HasPrefix(t, "class ") {
			name := pyDeclName(t[6:])
			if name != "" {
				return Symbol{Name: name, Kind: SymClass, Line: line}, true
			}
		}
	case "javascript", "typescript":
		if strings.HasPrefix(t, "function ") {
			name := jsDeclName(t[9:])
			if name != "" {
				return Symbol{Name: name, Kind: SymFunc, Line: line}, true
			}
		}
		if strings.HasPrefix(t, "class ") {
			name := jsDeclName(t[6:])
			if name != "" {
				return Symbol{Name: name, Kind: SymClass, Line: line}, true
			}
		}
	case "jvm":
		if strings.HasPrefix(t, "fun ") || strings.HasPrefix(t, "def ") {
			name := jvmDeclName(t)
			if name != "" {
				return Symbol{Name: name, Kind: SymFunc, Line: line}, true
			}
		}
		if strings.HasPrefix(t, "class ") || strings.HasPrefix(t, "interface ") {
			name := jvmDeclName(t)
			if name != "" {
				return Symbol{Name: name, Kind: SymClass, Line: line}, true
			}
		}
	}
	return Symbol{}, false
}

func goDeclName(s string, stop byte) string {
	if i := strings.IndexByte(s, stop); i > 0 {
		return strings.TrimSpace(s[:i])
	}
	return ""
}

func pyDeclName(s string) string {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '(' || c == ' ' || c == ':' {
			return s[:i]
		}
	}
	return s
}

func jsDeclName(s string) string {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '(' || c == ' ' || c == '<' || c == '=' {
			return s[:i]
		}
	}
	return s
}

func jvmDeclName(s string) string {
	parts := strings.Fields(s)
	if len(parts) >= 2 {
		return strings.TrimSuffix(parts[1], "(")
	}
	return ""
}

// matchImport extracts an import target from a trimmed line, if any.
func matchImport(lang, t string) string {
	switch lang {
	case "go":
		if strings.HasPrefix(t, "import \"") {
			return strings.Trim(strings.TrimPrefix(t, "import "), "\"")
		}
	case "python":
		if strings.HasPrefix(t, "import ") {
			return strings.TrimSpace(strings.TrimPrefix(t, "import "))
		}
		if strings.HasPrefix(t, "from ") {
			return strings.TrimSpace(strings.TrimPrefix(t, "from "))
		}
	case "javascript", "typescript":
		if strings.HasPrefix(t, "import ") {
			return t
		}
	}
	return ""
}
