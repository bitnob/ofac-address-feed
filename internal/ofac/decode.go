package ofac

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// skipBOM discards a leading UTF-8 byte-order mark if present.
func skipBOM(br *bufio.Reader) *bufio.Reader {
	if b, err := br.Peek(3); err == nil && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		_, _ = br.Discard(3)
	}
	return br
}

// utf8CharsetReader accepts only UTF-8 / US-ASCII (the advanced export is always
// utf-8) and rejects anything else instead of risking a silent mis-decode.
func utf8CharsetReader(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return input, nil
	default:
		return nil, fmt.Errorf("unsupported charset %q (expected utf-8)", charset)
	}
}
