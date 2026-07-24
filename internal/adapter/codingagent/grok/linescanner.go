package grok

import (
	"bufio"
	"bytes"
	"io"
)

// utf8BOM is the UTF-8 byte-order-mark, optionally emitted by some CLI
// runtimes (notably on Windows). It is stripped once from the head of the
// stream so it does not corrupt the first JSON line.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// lineScanner reads newline-delimited bytes without [bufio.Scanner]'s default
// 64KiB token limit (Grok streaming lines can be large — e.g. long message
// deltas). It tolerates CRLF and LF line endings and a leading UTF-8 BOM. Next
// returns the next line without the trailing newline; (nil, false) means EOF.
type lineScanner struct {
	r       *bufio.Reader
	out     []byte
	atEOF   bool
	bomRead bool
}

func newLineScanner(r io.Reader) *lineScanner { return &lineScanner{r: bufio.NewReader(r)} }

// Next returns the next line (without trailing CR/LF) and whether more data may
// follow. A nil line with hasMore == false means EOF. Long lines are accumulated
// across ReadLine fragments.
func (s *lineScanner) Next() (line []byte, hasMore bool) {
	if s.atEOF {
		return nil, false
	}
	frag, isPrefix, err := s.r.ReadLine()
	if err != nil {
		s.atEOF = true
		return stripBOMOnce(s, frag), false // frag may hold a final line w/o newline
	}
	frag = stripBOMOnce(s, frag)
	if !isPrefix {
		return append([]byte(nil), frag...), true
	}
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

// stripBOMOnce removes a leading UTF-8 BOM from the first fragment read. It is
// a no-op after the first call.
func stripBOMOnce(s *lineScanner, frag []byte) []byte {
	if s.bomRead {
		return frag
	}
	s.bomRead = true
	return bytes.TrimPrefix(frag, utf8BOM)
}
