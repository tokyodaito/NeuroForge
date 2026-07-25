package repoinfo

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// EstimateTokens returns a deterministic, conservative estimate of the token
// count of s. We never call a provider tokenizer (rule §22.6: no LLM in this
// package). The estimate splits on whitespace and CJK runes and adds a small
// overhead for punctuation — close enough to bound the context budget (§22.1).
// ~4 characters per token is a stable cross-model approximation.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	// Fast path: char-based estimate for large blobs.
	if len(s) > 32*1024 {
		return len(s) / 4
	}
	tokens := 0
	inWord := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if inWord {
				tokens++
				inWord = false
			}
			continue
		}
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			tokens++ // CJK: roughly one token per rune
			inWord = false
			continue
		}
		if isPunct(r) {
			tokens++
			inWord = false
			continue
		}
		inWord = true
	}
	if inWord {
		tokens++
	}
	if tokens == 0 && len(s) > 0 {
		tokens = len(s) / 4
	}
	return tokens
}

func isPunct(r rune) bool {
	switch r {
	case '.', ',', ';', ':', '(', ')', '[', ']', '{', '}', '<', '>', '=', '+',
		'-', '*', '/', '&', '|', '!', '?', '%', '@', '#', '$', '^', '~', '`',
		'"', '\'', '\\':
		return true
	}
	return false
}

// --- file read helpers ---

func openRead(root, rel string) *os.File {
	f, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil
	}
	return f
}

func mustOpen(root, rel string) *os.File {
	return openRead(root, rel)
}

func readText(f *os.File, maxLines int) string {
	if f == nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	var b strings.Builder
	n := 0
	for sc.Scan() {
		b.WriteString(sc.Text())
		b.WriteByte('\n')
		n++
		if maxLines > 0 && n >= maxLines {
			break
		}
	}
	return b.String()
}
