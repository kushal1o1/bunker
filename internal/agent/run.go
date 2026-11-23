package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"filippo.io/age"
	"github.com/kushal1o1/bunker/internal/backup"
	"github.com/kushal1o1/bunker/internal/execx"
)

// Status is <name>/status.json in S3, rewritten after every run, success or
// failure, so the laptop can see each agent's health without contacting it.
type Status struct {
	Agent     string    `json:"agent"`
	Version   string    `json:"version"`
	Started   time.Time `json:"started"`
	Finished  time.Time `json:"finished"`
	OK        bool      `json:"ok"`
	Snapshot  string    `json:"snapshot,omitempty"`
	Artifacts int       `json:"artifacts"`
	Size      int64     `json:"size"`
	Error     string    `json:"error,omitempty"`
}

// ServerFiles stores status.json and check.json next to the snapshots.
// *backup.S3Store implements it.
type ServerFiles interface {
	PutServerFile(ctx context.Context, server, name string, data []byte) error
}

// Runner takes one snapshot of this machine.
type Runner struct {
	Cfg     *Config
	Store   backup.Store
	Status  ServerFiles
	Exec    execx.Executor
	Log     io.Writer
	Version string
}

// RunOnce backs up everything in the config once, then writes status.json.
// Scheduling is left to the host: a systemd timer or cron runs
// `bunker agent run` when a backup is due.
// The returned error is the backup's; a failure to write the status is only
// logged, since the snapshot itself is what matters.
func (r *Runner) RunOnce(ctx context.Context) (*Status, error) {
	name := r.Cfg.Agent.Name
	st := &Status{Agent: name, Version: r.Version, Started: time.Now().UTC()}
	r.logf("%s: backup started", name)

	man, err := r.snapshot(ctx)
	st.Finished = time.Now().UTC()
	if err != nil {
		st.Error = err.Error()
		r.logf("%s: backup FAILED after %s: %v", name, elapsed(st), err)
	} else {
		st.OK = true
		st.Snapshot = man.Snapshot
		st.Artifacts = len(man.Artifacts)
		for _, a := range man.Artifacts {
			st.Size += a.Size
		}
		r.logf("%s: snapshot %s complete: %d artifact(s), %s in %s",
			name, st.Snapshot, st.Artifacts, backup.HumanBytes(st.Size), elapsed(st))
	}
	r.writeStatus(ctx, st)
	return st, err
}

func (r *Runner) snapshot(ctx context.Context) (*backup.Manifest, error) {
	recipient, err := age.ParseX25519Recipient(r.Cfg.Encryption.Recipient)
	if err != nil {
		return nil, fmt.Errorf("encryption.recipient: %w", err)
	}
	return backup.TakeSnapshot(ctx, r.Exec, r.Store, r.Cfg.Agent.Name, r.Cfg.Artifacts(), recipient, r.event)
}

func (r *Runner) event(e backup.Event) {
	switch e.Kind {
	case backup.ArtifactStarted:
		r.logf("%s: [%d/%d] %s", e.Server, e.Index, e.Total, e.Artifact)
	case backup.ArtifactDone:
		r.logf("%s: [%d/%d] %s done, %s in %s", e.Server, e.Index, e.Total, e.Artifact,
			backup.HumanBytes(e.Size), e.Elapsed.Round(100*time.Millisecond))
	}
}

// writeStatus runs even when ctx was cancelled (SIGTERM is a reason to
// record a failed run), with its own short deadline.
func (r *Runner) writeStatus(ctx context.Context, st *Status) {
	if r.Status == nil {
		return
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		r.logf("%s: encode status: %v", st.Agent, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := r.Status.PutServerFile(ctx, st.Agent, backup.StatusName, append(data, '\n')); err != nil {
		r.logf("%s: could not write %s: %v", st.Agent, backup.StatusName, err)
	}
}

func (r *Runner) logf(format string, args ...any) {
	if r.Log == nil {
		return
	}
	fmt.Fprintf(r.Log, "%s %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

func elapsed(st *Status) time.Duration {
	return st.Finished.Sub(st.Started).Round(100 * time.Millisecond)
}
