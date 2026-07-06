package domain

import (
	"math"
	"testing"
	"time"
)

func TestEntryScore(t *testing.T) {
	quant := Habit{Kind: Quantified, Target: 20}
	cases := []struct {
		e    Entry
		want float64
	}{
		{Entry{Status: StatusDone}, 1},
		{Entry{Status: StatusPartial, Amount: 12}, 0.6},
		{Entry{Status: StatusPartial, Amount: 30}, 1}, // clamped
		{Entry{Status: StatusSkip}, 0},
		{Entry{Status: StatusFreeze}, 0}, // freeze saves the streak, not the score
	}
	for _, c := range cases {
		if got := EntryScore(quant, c.e); got != c.want {
			t.Errorf("EntryScore(%+v) = %v, want %v", c.e, got, c.want)
		}
	}
}

func TestDayScore(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	day := Day("2026-07-07")
	paused := created
	habits := []Habit{
		{ID: 1, Kind: Check, Schedule: Daily, CreatedAt: created},
		{ID: 2, Kind: Quantified, Target: 20, Schedule: Daily, CreatedAt: created},
		{ID: 3, Kind: Check, Schedule: Daily, CreatedAt: created}, // will stay unlogged
		{ID: 4, Kind: Check, Schedule: Weekly, PerWeek: 3, CreatedAt: created},
		{ID: 5, Kind: Check, Schedule: Daily, CreatedAt: created, PausedAt: &paused},
	}
	entries := []Entry{
		{HabitID: 1, Day: day, Status: StatusDone},
		{HabitID: 2, Day: day, Status: StatusPartial, Amount: 12},
		{HabitID: 99, Day: "2026-07-06", Status: StatusDone}, // other day: ignored
	}
	// Unlogged weekly (4) and paused (5) excluded → (1 + 0.6 + 0) / 3.
	want := (1 + 0.6) / 3
	if got := DayScore(habits, entries, day); math.Abs(got-want) > 1e-9 {
		t.Errorf("DayScore = %v, want %v", got, want)
	}

	// A logged weekly habit joins the denominator.
	entries = append(entries, Entry{HabitID: 4, Day: day, Status: StatusDone})
	want = (1 + 0.6 + 1) / 4
	if got := DayScore(habits, entries, day); math.Abs(got-want) > 1e-9 {
		t.Errorf("DayScore with weekly = %v, want %v", got, want)
	}

	// Habits created after the day don't count.
	late := []Habit{{ID: 1, Kind: Check, Schedule: Daily,
		CreatedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)}}
	if got := DayScore(late, entries, day); got != 0 {
		t.Errorf("DayScore before creation = %v, want 0", got)
	}

	if got := DayScore(nil, nil, day); got != 0 {
		t.Errorf("DayScore empty = %v, want 0", got)
	}
}
