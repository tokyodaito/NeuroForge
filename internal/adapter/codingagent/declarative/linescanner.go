package declarative

import (
	"bufio"
	"io"
)

// lineScanner reads newline-delimited bytes without bufio.Scanner's default
// 64KiB token limit (agent lines can be large). Next returns the next line
// (without the trailing newline) and hasMore reports whether more data may
// follow. When the stream is at EOF with no pending line, Next returns
// (nil, false).
type lineScanner struct {
	r     *bufio.Reader
	out   []byte
	atEOF bool
}

func newLineScanner(r io.Reader) *lineScanner { return &lineScanner{r: bufio.NewReader(r)} }

// Next reads the next line. Returns (line, hasMore). A nil line with hasMore
// false means EOF.
func (s *lineScanner) Next() (line []byte, hasMore bool) {
	if s.atEOF {
		return nil, false
	}
	frag, isPrefix, err := s.r.ReadLine()
	if err != nil {
		s.atEOF = true
		return frag, false // frag may be non-nil for a final line without newline
	}
	if !isPrefix {
		return append([]byte(nil), frag...), true
	}
	// Long line: accumulate until the final fragment.
	acc := append([]byte(nil), frag...)
	for isPrefix {
		frag, isPrefix, err = s.r.ReadLine()
		if err != nil {
			break
		}
		acc = append(acc, frag...)
	}
	return acc, true
}
