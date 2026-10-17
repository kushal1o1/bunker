package retention

import (
	"reflect"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func items(times ...string) []Item {
	out := make([]Item, len(times))
	for i, s := range times {
		out[i] = Item{ID: s, Time: at(s)}
	}
	return out
}

func kept(ds []Decision) []string {
	var ids []string
	for _, d := range ds {
		if d.Keep {
			ids = append(ids, d.ID)
		}
	}
	return ids
}

// The worked example from docs/retention.md: daily backups through October
// (two on the 17th) plus a few older ones, policy 7/4/6.
func TestApplyWorkedExample(t *testing.T) {
	in := items(
		"2026-10-17 18:00", "2026-10-17 06:00",
		"2026-10-16 09:00", "2026-10-15 09:00", "2026-10-14 09:00", "2026-10-13 09:00",
		"2026-10-12 09:00", "2026-10-11 09:00", "2026-10-10 09:00", "2026-10-09 09:00",
		"2026-10-08 09:00", "2026-10-07 09:00", "2026-10-06 09:00", "2026-10-05 09:00",
		"2026-10-04 09:00", "2026-10-03 09:00", "2026-10-02 09:00", "2026-10-01 09:00",
		"2026-09-20 09:00", "2026-09-02 09:00", "2026-08-15 09:00", "2026-07-01 09:00",
		"2026-01-10 09:00",
	)
	ds := Apply(Policy{Daily: 7, Weekly: 4, Monthly: 6}, in)

	want := []string{
		"2026-10-17 18:00",
		"2026-10-16 09:00", "2026-10-15 09:00", "2026-10-14 09:00", "2026-10-13 09:00",
		"2026-10-12 09:00", "2026-10-11 09:00",
		"2026-10-04 09:00",
		"2026-09-20 09:00", "2026-08-15 09:00", "2026-07-01 09:00",
		"2026-01-10 09:00",
	}
	if got := kept(ds); !reflect.DeepEqual(got, want) {
		t.Fatalf("kept =\n  %v\nwant\n  %v", got, want)
	}

	reasons := map[string][]string{}
	for _, d := range ds {
		reasons[d.ID] = d.Reasons
	}
	checks := map[string][]string{
		"2026-10-17 18:00": {"daily 1", "weekly 1", "monthly 1"},
		"2026-10-17 06:00": nil,
		"2026-10-11 09:00": {"daily 7", "weekly 2"},
		"2026-10-04 09:00": {"weekly 3"},
		"2026-09-20 09:00": {"weekly 4", "monthly 2"},
		"2026-09-02 09:00": nil,
		"2026-01-10 09:00": {"monthly 5"},
	}
	for id, want := range checks {
		if got := reasons[id]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s reasons = %v, want %v", id, got, want)
		}
	}
}

func TestApplyNewestOfDayWins(t *testing.T) {
	ds := Apply(Policy{Daily: 1}, items("2026-10-17 06:00", "2026-10-17 18:00", "2026-10-17 12:00"))
	if got, want := kept(ds), []string{"2026-10-17 18:00"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("kept = %v, want %v", got, want)
	}
}

// A long gap in backups must not prune the last good days.
func TestApplyCountsSnapshotDaysNotCalendarDays(t *testing.T) {
	ds := Apply(Policy{Daily: 3}, items("2026-01-03 09:00", "2026-01-02 09:00", "2026-01-01 09:00", "2025-12-31 09:00"))
	if got, want := kept(ds), []string{"2026-01-03 09:00", "2026-01-02 09:00", "2026-01-01 09:00"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("kept = %v, want %v", got, want)
	}
}

func TestApplyAlwaysKeepsNewest(t *testing.T) {
	ds := Apply(Policy{}, items("2026-10-16 09:00", "2026-10-17 09:00"))
	if got, want := kept(ds), []string{"2026-10-17 09:00"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("kept = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(ds[0].Reasons, []string{"newest"}) {
		t.Errorf("reasons = %v, want [newest]", ds[0].Reasons)
	}
}

func TestApplyEmpty(t *testing.T) {
	if ds := Apply(Policy{Daily: 7}, nil); len(ds) != 0 {
		t.Fatalf("got %d decisions, want 0", len(ds))
	}
}

// ISO weeks run Monday to Sunday and cross year boundaries: Mon 2025-12-29
// and Sun 2026-01-04 are both in 2026-W01.
func TestApplyISOWeekAcrossYear(t *testing.T) {
	ds := Apply(Policy{Weekly: 1}, items("2026-01-04 09:00", "2025-12-29 09:00"))
	if got, want := kept(ds), []string{"2026-01-04 09:00"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("kept = %v, want %v", got, want)
	}
}
