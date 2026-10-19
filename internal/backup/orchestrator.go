package backup

import (
	"context"
	"sync"
	"time"
)

// ServerResult is the outcome of one server in a RunMany call. Exactly one of
// Result and Err is set.
type ServerResult struct {
	Server  string
	Result  *Result
	Err     error
	Elapsed time.Duration
}

// RunMany backs up every server with at most workers running at once and
// returns one result per server, in the order given.
//
// Every server is attempted: a failure is recorded in its result and does not
// stop the others. Cancelling ctx stops new servers from starting (they get
// ctx.Err()) and cancels the ones in flight, which delete their own partial
// snapshots. onDone, if non-nil, is called as each server finishes, from that
// server's goroutine.
func RunMany(ctx context.Context, r *Runner, servers []string, workers int, onDone func(ServerResult)) []ServerResult {
	if workers < 1 {
		workers = 1
	}
	results := make([]ServerResult, len(servers))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup

	for i, name := range servers {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i] = ServerResult{Server: name, Err: ctx.Err()}
				if onDone != nil {
					onDone(results[i])
				}
				return
			}
			// A slot may free up in the same instant ctx is cancelled; select
			// picks randomly, so check again before starting real work.
			if err := ctx.Err(); err != nil {
				results[i] = ServerResult{Server: name, Err: err}
				if onDone != nil {
					onDone(results[i])
				}
				return
			}

			start := time.Now()
			res, err := r.Run(ctx, name)
			results[i] = ServerResult{Server: name, Result: res, Err: err, Elapsed: time.Since(start)}
			if onDone != nil {
				onDone(results[i])
			}
		}(i, name)
	}
	wg.Wait()
	return results
}
