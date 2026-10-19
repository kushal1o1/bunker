package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/kushal1o1/bunker/internal/backup"
)

// progressView shows a multi-server backup run as it happens. Event and Done
// are called from the backup goroutines, so implementations lock.
type progressView interface {
	Event(backup.Event)
	Done(backup.ServerResult)
	Close()
}

type phase int

const (
	phaseWaiting phase = iota
	phaseConnecting
	phaseRunning
	phaseDone
	phaseFailed
	phaseCancelled
)

// serverState is everything one table row needs.
type serverState struct {
	name     string
	phase    phase
	artifact string
	index    int
	total    int
	started  time.Time
	elapsed  time.Duration // set when finished
	size     int64
	count    int
	err      error
}

// --- live table (terminal) ------------------------------------------------

// liveTable redraws one row per server in place, using ANSI cursor movement.
// Rows never wrap (long text is truncated), so the line count stays fixed
// and moving the cursor up by that count always lands on the first row.
type liveTable struct {
	w      io.Writer
	mu     sync.Mutex
	states []*serverState
	byName map[string]*serverState
	drawn  int
	stop   chan struct{}
	wg     sync.WaitGroup
}

func newLiveTable(w io.Writer, servers []string) *liveTable {
	t := &liveTable{w: w, byName: map[string]*serverState{}, stop: make(chan struct{})}
	for _, n := range servers {
		s := &serverState{name: n}
		t.states = append(t.states, s)
		t.byName[n] = s
	}
	t.draw()
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				t.draw()
			case <-t.stop:
				return
			}
		}
	}()
	return t
}

func (t *liveTable) Event(e backup.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	applyEvent(t.byName[e.Server], e)
}

func (t *liveTable) Done(r backup.ServerResult) {
	t.mu.Lock()
	defer t.mu.Unlock()
	applyDone(t.byName[r.Server], r)
}

// Close stops the redraw loop and draws the final state once more.
func (t *liveTable) Close() {
	close(t.stop)
	t.wg.Wait()
	t.draw()
}

func (t *liveTable) draw() {
	t.mu.Lock()
	lines := renderTable(t.states, time.Now())
	t.mu.Unlock()

	var buf bytes.Buffer
	if t.drawn > 0 {
		fmt.Fprintf(&buf, "\x1b[%dA", t.drawn)
	}
	for _, l := range lines {
		buf.WriteString("\r\x1b[2K")
		buf.WriteString(l)
		buf.WriteByte('\n')
	}
	t.drawn = len(lines)
	t.w.Write(buf.Bytes())
}

// renderTable formats one aligned line per server. Pure, for tests.
func renderTable(states []*serverState, now time.Time) []string {
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	for _, s := range states {
		var status, progress, size, took string
		switch s.phase {
		case phaseWaiting:
			status = "waiting"
		case phaseConnecting:
			status, took = "connecting", fmtDur(now.Sub(s.started))
		case phaseRunning:
			status = truncate(s.artifact, 40)
			progress = fmt.Sprintf("%d/%d", s.index, s.total)
			took = fmtDur(now.Sub(s.started))
		case phaseDone:
			status = "done"
			progress = fmt.Sprintf("%d artifact%s", s.count, plural(s.count))
			size, took = backup.HumanBytes(s.size), fmtDur(s.elapsed)
		case phaseFailed:
			status, took = "failed: "+truncate(oneLine(s.err), 60), fmtDur(s.elapsed)
		case phaseCancelled:
			status = "cancelled"
		}
		fmt.Fprintf(w, "%s %s\t%s\t%s\t%s\t%s\n", symbol(s.phase), s.name, status, progress, size, took)
	}
	w.Flush()
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return lines
}

func symbol(p phase) string {
	switch p {
	case phaseConnecting, phaseRunning:
		return "⟳"
	case phaseDone:
		return "✓"
	case phaseFailed, phaseCancelled:
		return "✗"
	default:
		return "○"
	}
}

// --- line log (cron, pipes) -----------------------------------------------

// lineLog writes one prefixed line per notable event. Lines from different
// servers interleave but never split.
type lineLog struct {
	w     io.Writer
	hosts map[string]string
	mu    sync.Mutex
}

func newLineLog(w io.Writer, hosts map[string]string) *lineLog {
	return &lineLog{w: w, hosts: hosts}
}

func (l *lineLog) printf(server, format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, "[%s] "+format+"\n", append([]any{server}, args...)...)
}

func (l *lineLog) Event(e backup.Event) {
	switch e.Kind {
	case backup.Connecting:
		l.printf(e.Server, "connecting %s", l.hosts[e.Server])
	case backup.ArtifactDone:
		l.printf(e.Server, "ok %s %s %s", e.Artifact, backup.HumanBytes(e.Size), fmtDur(e.Elapsed))
	}
}

func (l *lineLog) Done(r backup.ServerResult) {
	switch {
	case r.Err == nil:
		l.printf(r.Server, "snapshot %s (%s) in %s", r.Result.Snapshot, backup.HumanBytes(resultSize(r.Result)), fmtDur(r.Elapsed))
	case errors.Is(r.Err, context.Canceled):
		l.printf(r.Server, "cancelled")
	default:
		l.printf(r.Server, "FAILED: %s", oneLine(r.Err))
	}
}

func (l *lineLog) Close() {}

// --- shared ---------------------------------------------------------------

func applyEvent(s *serverState, e backup.Event) {
	if s == nil {
		return
	}
	switch e.Kind {
	case backup.Connecting:
		s.phase, s.started = phaseConnecting, time.Now()
	case backup.ArtifactStarted:
		s.phase, s.artifact, s.index, s.total = phaseRunning, e.Artifact, e.Index, e.Total
	}
}

func applyDone(s *serverState, r backup.ServerResult) {
	if s == nil {
		return
	}
	s.elapsed = r.Elapsed
	switch {
	case r.Err == nil:
		s.phase, s.count, s.size = phaseDone, len(r.Result.Artifacts), resultSize(r.Result)
	case errors.Is(r.Err, context.Canceled):
		s.phase = phaseCancelled
	default:
		s.phase, s.err = phaseFailed, r.Err
	}
}

// printSummary is the final report, printed in both output modes.
func printSummary(w io.Writer, results []backup.ServerResult, elapsed time.Duration) error {
	ok := 0
	for _, r := range results {
		if r.Err == nil {
			ok++
		}
	}
	fmt.Fprintf(w, "\n%d/%d backed up in %s\n", ok, len(results), fmtDur(elapsed))

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, r := range results {
		switch {
		case r.Err == nil:
			fmt.Fprintf(tw, "  ✓ %s\t%s\t%s\n", r.Server, r.Result.Snapshot, backup.HumanBytes(resultSize(r.Result)))
		case errors.Is(r.Err, context.Canceled):
			fmt.Fprintf(tw, "  ✗ %s\tcancelled\n", r.Server)
		default:
			fmt.Fprintf(tw, "  ✗ %s\t%s\n", r.Server, oneLine(r.Err))
		}
	}
	return tw.Flush()
}

func resultSize(r *backup.Result) int64 {
	var n int64
	for _, a := range r.Artifacts {
		n += a.Size
	}
	return n
}

func fmtDur(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(100 * time.Millisecond).String()
}

func oneLine(err error) string {
	if err == nil {
		return ""
	}
	return strings.Join(strings.Fields(err.Error()), " ")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-3]) + "..."
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
