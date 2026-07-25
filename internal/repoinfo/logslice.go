package repoinfo

import (
	"strings"
)

// LogSlice is the trimmed, agent-ready view of a build/test log (spec §22.4).
// Models are NEVER sent the full enormous log. Instead they receive the exit
// code, the failing command, the first error, the relevant stack trace and a
// summary of the remaining errors — plus a link to the full log.
type LogSlice struct {
	ExitCode        int
	FailingCommand  string
	FirstError      string
	StackTrace      []string
	OtherErrors     []string
	FullLogLink     string
	EstimatedTokens int
}

// MaxLogTokens caps a log slice so it can never blow the context budget (§22.1).
const MaxLogTokens = 1500

// SliceLog reduces a raw log to a compact LogSlice (§22.4). The algorithm is
// deterministic and never calls an LLM (rule §22.6):
//   - the first line that looks like an error/failure is the FirstError;
//   - contiguous indented "at/..." lines after it form the StackTrace;
//   - subsequent distinct error lines are summarised (deduped) as OtherErrors.
//
// The slice is bounded so its token estimate stays within MaxLogTokens.
func SliceLog(raw string, exitCode int, failingCommand, fullLogLink string) LogSlice {
	lines := splitLines(raw)
	s := LogSlice{
		ExitCode:       exitCode,
		FailingCommand: failingCommand,
		FullLogLink:    fullLogLink,
	}

	firstErrIdx := -1
	for i, ln := range lines {
		if isErrorLine(ln) {
			s.FirstError = strings.TrimSpace(ln)
			firstErrIdx = i
			break
		}
	}

	// Stack trace = contiguous indented lines following the first error.
	if firstErrIdx >= 0 {
		for i := firstErrIdx + 1; i < len(lines); i++ {
			ln := lines[i]
			if isStackTraceLine(ln) {
				s.StackTrace = append(s.StackTrace, strings.TrimSpace(ln))
				if len(s.StackTrace) >= 8 {
					break
				}
				continue
			}
			if strings.TrimSpace(ln) == "" {
				continue
			}
			break
		}
	}

	// Other errors: distinct error lines after the first (deduped, capped).
	seen := map[string]bool{s.FirstError: true}
	for i := firstErrIdx + 1; i < len(lines); i++ {
		ln := strings.TrimSpace(lines[i])
		if isErrorLine(ln) && !seen[ln] {
			s.OtherErrors = append(s.OtherErrors, ln)
			seen[ln] = true
			if len(s.OtherErrors) >= 6 {
				break
			}
		}
	}

	s.EstimatedTokens = EstimateTokens(s.render())
	// Bound the slice to the token budget: drop other-errors first, then trace.
	for s.EstimatedTokens > MaxLogTokens && len(s.OtherErrors) > 0 {
		s.OtherErrors = s.OtherErrors[:len(s.OtherErrors)-1]
		s.EstimatedTokens = EstimateTokens(s.render())
	}
	for s.EstimatedTokens > MaxLogTokens && len(s.StackTrace) > 2 {
		s.StackTrace = s.StackTrace[:len(s.StackTrace)-1]
		s.EstimatedTokens = EstimateTokens(s.render())
	}
	return s
}

// Render produces the human/agent-readable text of the slice (§22.4).
func (s LogSlice) Render() string { return s.render() }

func (s LogSlice) render() string {
	var b strings.Builder
	if s.ExitCode != 0 {
		b.WriteString("exit: ")
		b.WriteString(itoa(s.ExitCode))
		b.WriteByte('\n')
	}
	if s.FailingCommand != "" {
		b.WriteString("command: ")
		b.WriteString(s.FailingCommand)
		b.WriteByte('\n')
	}
	if s.FirstError != "" {
		b.WriteString("first error: ")
		b.WriteString(s.FirstError)
		b.WriteByte('\n')
	}
	if len(s.StackTrace) > 0 {
		b.WriteString("stack trace:\n")
		for _, ln := range s.StackTrace {
			b.WriteString("  ")
			b.WriteString(ln)
			b.WriteByte('\n')
		}
	}
	if len(s.OtherErrors) > 0 {
		b.WriteString("other errors (summary):\n")
		for _, ln := range s.OtherErrors {
			b.WriteString("  - ")
			b.WriteString(ln)
			b.WriteByte('\n')
		}
	}
	if s.FullLogLink != "" {
		b.WriteString("full log: ")
		b.WriteString(s.FullLogLink)
		b.WriteByte('\n')
	}
	return b.String()
}

func isErrorLine(line string) bool {
	l := strings.ToLower(line)
	if strings.Contains(l, "error") || strings.Contains(l, "failed") ||
		strings.Contains(l, "failure") || strings.Contains(l, "panic") ||
		strings.Contains(l, "fatal") || strings.Contains(l, "exception") ||
		strings.Contains(l, "cannot use") || strings.Contains(l, "undefined") ||
		strings.Contains(l, "mismatched") || strings.Contains(l, "not used") ||
		strings.Contains(l, "declared and not") || strings.Contains(l, "not enough arguments") ||
		strings.Contains(l, "too many arguments") || strings.Contains(l, "traceback") {
		// Ignore verbose progress lines that happen to contain "error".
		if strings.Contains(l, "0 error") || strings.Contains(l, "no errors") {
			return false
		}
		return true
	}
	// Compiler diagnostics: "<file>:<line>:" / "<file>:<line>:<col>:".
	if isDiagnosticLine(line) {
		return true
	}
	return false
}

// isDiagnosticLine recognises the common "<path>:<line>:[<col>:]" diagnostic
// prefix emitted by go/rustc/javac/gcc/pytest/etc.
func isDiagnosticLine(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	// Find the first colon.
	c1 := strings.IndexByte(t, ':')
	if c1 <= 0 {
		return false
	}
	rest := t[c1+1:]
	c2 := strings.IndexByte(rest, ':')
	if c2 < 0 {
		// "<path>:<line>" with trailing message is still a diagnostic.
		// Require the segment after the first colon to start with a digit.
		return startsAndThen(rest)
	}
	// "<path>:<line>:<col>:..." — line number segment must be digits.
	return startsAndThen(rest[:c2])
}

func startsAndThen(s string) bool {
	if s == "" {
		return false
	}
	// Must start with one or more digits.
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i > 0
}

func isStackTraceLine(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	// Indented continuation OR begins with a stack-trace marker.
	if line != t && (line[0] == ' ' || line[0] == '\t') {
		lt := strings.ToLower(t)
		if strings.HasPrefix(lt, "at ") || strings.HasPrefix(t, "...") ||
			strings.HasPrefix(t, "(") || strings.Contains(lt, ".go:") ||
			strings.Contains(lt, ".java:") || strings.Contains(lt, ".py") ||
			strings.Contains(lt, ".ts:") || strings.Contains(lt, ".js:") {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	// Normalise CRLF.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Split(s, "\n")
}

func itoa(n int) string {
	// Avoid strconv to keep the package import-light; small helper.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
