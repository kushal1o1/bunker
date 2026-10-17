// Package retention decides which snapshots a daily / weekly / monthly policy
// keeps. It is pure: no filesystem, no config, just times in and decisions
// out, so the rules can be tested exhaustively.
package retention

import (
	"fmt"
	"sort"
	"time"
)

// Policy is how many days, weeks and months to keep one snapshot for.
type Policy struct {
	Daily, Weekly, Monthly int
}

// Item is one snapshot to decide on.
type Item struct {
	ID   string
	Time time.Time
}

// Decision is the outcome for one Item. Reasons lists every rule that keeps
// it ("daily 1", "weekly 2", ...); a snapshot with no reasons is deleted.
type Decision struct {
	Item
	Keep    bool
	Reasons []string
}

// Apply runs the policy over items and returns one decision per item, newest
// first.
//
// Each rule walks the snapshots from newest to oldest and keeps the newest
// snapshot of every day (week, month) until it has covered N of them. The
// count is of periods that HAVE a snapshot, not calendar time: if backups
// stop for three weeks, the last seven good days are still kept instead of
// "the last seven calendar days" being empty and everything being pruned.
//
// The newest snapshot is always kept, even under a policy of all zeros.
// Times are compared in UTC, matching snapshot IDs.
func Apply(p Policy, items []Item) []Decision {
	out := make([]Decision, len(items))
	for i, it := range items {
		out[i] = Decision{Item: it}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })

	keepNewestPer(out, "daily", p.Daily, func(t time.Time) string {
		return t.Format("2006-01-02")
	})
	keepNewestPer(out, "weekly", p.Weekly, func(t time.Time) string {
		y, w := t.ISOWeek()
		return fmt.Sprintf("%d-W%02d", y, w)
	})
	keepNewestPer(out, "monthly", p.Monthly, func(t time.Time) string {
		return t.Format("2006-01")
	})

	if len(out) > 0 && !out[0].Keep {
		out[0].Keep = true
		out[0].Reasons = append(out[0].Reasons, "newest")
	}
	return out
}

// keepNewestPer marks the first (newest) decision in each of the first n
// distinct periods. ds must be sorted newest first.
func keepNewestPer(ds []Decision, rule string, n int, period func(time.Time) string) {
	seen := map[string]bool{}
	for i := range ds {
		if len(seen) >= n {
			return
		}
		key := period(ds[i].Time.UTC())
		if seen[key] {
			continue
		}
		seen[key] = true
		ds[i].Keep = true
		ds[i].Reasons = append(ds[i].Reasons, fmt.Sprintf("%s %d", rule, len(seen)))
	}
}
