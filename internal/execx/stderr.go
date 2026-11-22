package execx

import (
	"bytes"
	"fmt"
)

// MaxStderr bounds how much stderr is kept for an error message. It is only
// read when a command fails, and a chatty command must not be able to grow
// it without limit.
const MaxStderr = 64 << 10

// LimitedBuffer keeps the first Limit bytes and discards the rest, so a
// runaway command cannot exhaust memory while still reporting why it failed.
type LimitedBuffer struct {
	Limit int

	buf       bytes.Buffer
	dropped   int
	truncated bool
}

func (l *LimitedBuffer) Write(p []byte) (int, error) {
	if room := l.Limit - l.buf.Len(); room > 0 {
		if len(p) > room {
			l.buf.Write(p[:room])
			l.dropped = len(p) - room
			l.truncated = true
			return len(p), nil
		}
		l.buf.Write(p)
		return len(p), nil
	}
	l.dropped += len(p)
	l.truncated = true
	return len(p), nil
}

// Dropped is how many bytes were discarded.
func (l *LimitedBuffer) Dropped() int { return l.dropped }

func (l *LimitedBuffer) String() string {
	if !l.truncated {
		return l.buf.String()
	}
	return l.buf.String() + fmt.Sprintf("\n... [%d more bytes of stderr truncated]", l.dropped)
}
