package cli

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kushal1o1/bunker/internal/backup"
)

func okResult(server, snap string, sizes ...int64) backup.ServerResult {
	res := &backup.Result{Server: server, Snapshot: snap}
	for _, s := range sizes {
		res.Artifacts = append(res.Artifacts, backup.ArtifactEntry{Size: s})
	}
	return backup.ServerResult{Server: server, Result: res, Elapsed: 8100 * time.Millisecond}
}

func TestRenderTable(t *testing.T) {
	now := time.Now()
	states := []*serverState{
		{name: "kaski", phase: phaseDone, count: 2, size: 2048, elapsed: 8100 * time.Millisecond},
		{name: "web-01", phase: phaseRunning, artifact: "files/var/www/media", index: 1, total: 3, started: now.Add(-3200 * time.Millisecond)},
		{name: "web-02", phase: phaseWaiting},
		{name: "db-01", phase: phaseFailed, err: errors.New("ssh: connection\nrefused"), elapsed: time.Second},
		{name: "db-02", phase: phaseCancelled},
	}
	got := renderTable(states, now)
	want := []string{
		"✓ kaski   done                             2 artifacts  2.0 KB  8.1s",
		"⟳ web-01  files/var/www/media              1/3                  3.2s",
		"○ web-02  waiting",
		"✗ db-01   failed: ssh: connection refused                       1s",
		"✗ db-02   cancelled",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("renderTable =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestRenderTableTruncatesLongText(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := renderTable([]*serverState{{name: "s", phase: phaseFailed, err: errors.New(long)}}, time.Now())
	if len(got) != 1 || len(got[0]) > 100 {
		t.Fatalf("row not truncated: %d chars", len(got[0]))
	}
}

func TestLineLog(t *testing.T) {
	var buf bytes.Buffer
	l := newLineLog(&buf, map[string]string{"web": "203.0.113.20"})
	l.Event(backup.Event{Server: "web", Kind: backup.Connecting})
	l.Event(backup.Event{Server: "web", Kind: backup.ArtifactStarted, Artifact: "postgres/db"})
	l.Event(backup.Event{Server: "web", Kind: backup.ArtifactDone, Artifact: "postgres/db", Size: 1024, Elapsed: 1500 * time.Millisecond})
	l.Done(okResult("web", "2026-10-17T09-12-03", 1024))
	l.Done(backup.ServerResult{Server: "db", Err: errors.New("ssh: refused")})
	l.Done(backup.ServerResult{Server: "late", Err: context.Canceled})

	want := `[web] connecting 203.0.113.20
[web] ok postgres/db 1.0 KB 1.5s
[web] snapshot 2026-10-17T09-12-03 (1.0 KB) in 8.1s
[db] FAILED: ssh: refused
[late] cancelled
`
	if got := buf.String(); got != want {
		t.Fatalf("lineLog =\n%s\nwant\n%s", got, want)
	}
}

func TestPrintSummary(t *testing.T) {
	var buf bytes.Buffer
	results := []backup.ServerResult{
		okResult("kaski", "2026-10-17T09-12-03", 1024, 1024),
		{Server: "db-01", Err: errors.New("ssh: connection refused")},
		{Server: "db-02", Err: context.Canceled},
	}
	if err := printSummary(&buf, results, 12300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	want := `
1/3 backed up in 12.3s
  ✓ kaski  2026-10-17T09-12-03  2.0 KB
  ✗ db-01  ssh: connection refused
  ✗ db-02  cancelled
`
	if got := buf.String(); got != want {
		t.Fatalf("printSummary =\n%q\nwant\n%q", got, want)
	}
}

// Events arrive from one goroutine per server while the ticker redraws; run
// with -race. The final frame must show every server finished.
func TestLiveTableConcurrentEvents(t *testing.T) {
	var buf bytes.Buffer
	names := []string{"a", "b", "c", "d"}
	lt := newLiveTable(&buf, names)

	var wg sync.WaitGroup
	for _, n := range names {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			lt.Event(backup.Event{Server: n, Kind: backup.Connecting})
			for i := 1; i <= 3; i++ {
				lt.Event(backup.Event{Server: n, Kind: backup.ArtifactStarted, Artifact: "files/x", Index: i, Total: 3})
				time.Sleep(40 * time.Millisecond)
			}
			lt.Done(okResult(n, "2026-10-17T09-12-03", 10))
		}(n)
	}
	wg.Wait()
	lt.Close()

	out := buf.String()
	// Every redraw after the first starts by moving up one line per server.
	last := out[strings.LastIndex(out, "\x1b[4A"):]
	for _, n := range names {
		if !strings.Contains(last, "✓ "+n) {
			t.Errorf("final frame missing done row for %s:\n%s", n, last)
		}
	}
}

func TestDedupe(t *testing.T) {
	got := dedupe([]string{"a", "b", "a", "c", "b"})
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("dedupe = %v, want %v", got, want)
	}
}
