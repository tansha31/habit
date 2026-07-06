package domain

import (
	"testing"
	"time"
)

func ent(day Day, st Status) Entry { return Entry{Day: day, Status: st} }

func days(st Status, ds ...Day) []Entry {
	var out []Entry
	for _, d := range ds {
		out = append(out, ent(d, st))
	}
	return out
}

func TestDailyStreak(t *testing.T) {
	daily := Habit{Schedule: Daily}
	today := Day("2026-07-07")
	cases := []struct {
		name    string
		entries []Entry
		want    Streak
	}{
		{"empty", nil, Streak{}},
		{"three consecutive ending today",
			days(StatusDone, "2026-07-05", "2026-07-06", "2026-07-07"),
			Streak{Current: 3, Best: 3, LastDay: "2026-07-07"}},
		{"gap resets current, keeps best",
			days(StatusDone, "2026-07-01", "2026-07-02", "2026-07-03", "2026-07-06", "2026-07-07"),
			Streak{Current: 2, Best: 3, LastDay: "2026-07-07"}},
		{"ended yesterday still alive",
			days(StatusDone, "2026-07-05", "2026-07-06"),
			Streak{Current: 2, Best: 2, LastDay: "2026-07-06"}},
		{"ended two days ago is dead",
			days(StatusDone, "2026-07-04", "2026-07-05"),
			Streak{Current: 0, Best: 2}},
		{"freeze connects without growing",
			append(days(StatusDone, "2026-07-03", "2026-07-04", "2026-07-06", "2026-07-07"),
				ent("2026-07-05", StatusFreeze)),
			Streak{Current: 4, Best: 4, LastDay: "2026-07-07"}},
		{"pause connects without growing",
			append(days(StatusDone, "2026-07-04", "2026-07-06", "2026-07-07"),
				ent("2026-07-05", StatusPause)),
			Streak{Current: 3, Best: 3, LastDay: "2026-07-07"}},
		{"freeze tail keeps chain alive at yesterday",
			append(days(StatusDone, "2026-07-03", "2026-07-04", "2026-07-05"),
				ent("2026-07-06", StatusFreeze)),
			Streak{Current: 3, Best: 3, LastDay: "2026-07-06"}},
		{"partial breaks the chain",
			[]Entry{ent("2026-07-05", StatusDone), ent("2026-07-06", StatusPartial), ent("2026-07-07", StatusDone)},
			Streak{Current: 1, Best: 1, LastDay: "2026-07-07"}},
		{"skip breaks the chain",
			[]Entry{ent("2026-07-05", StatusDone), ent("2026-07-06", StatusSkip), ent("2026-07-07", StatusDone)},
			Streak{Current: 1, Best: 1, LastDay: "2026-07-07"}},
	}
	for _, c := range cases {
		if got := ComputeStreak(daily, c.entries, today, time.Monday); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestWeeklyStreak(t *testing.T) {
	// 2026-06-01, -08, -15 are Mondays; today is Tue 2026-06-16.
	weekly := Habit{Schedule: Weekly, PerWeek: 3}
	today := Day("2026-06-16")
	cases := []struct {
		name    string
		entries []Entry
		want    Streak
	}{
		{"two full weeks, current week in progress",
			days(StatusDone, "2026-06-01", "2026-06-02", "2026-06-03",
				"2026-06-08", "2026-06-09", "2026-06-10", "2026-06-15"),
			Streak{Current: 2, Best: 2, LastDay: "2026-06-08"}},
		{"current week already qualified",
			days(StatusDone, "2026-06-08", "2026-06-09", "2026-06-10",
				"2026-06-15", "2026-06-15", "2026-06-16"), // dup day is impossible in DB; harmless here
			Streak{Current: 2, Best: 2, LastDay: "2026-06-15"}},
		{"under-target week does not qualify",
			days(StatusDone, "2026-06-01", "2026-06-02",
				"2026-06-08", "2026-06-09", "2026-06-10"),
			Streak{Current: 1, Best: 1, LastDay: "2026-06-08"}},
		{"qualifying week too old is dead",
			days(StatusDone, "2026-06-01", "2026-06-02", "2026-06-03"),
			Streak{Current: 0, Best: 1}},
		{"gap week resets current, keeps best",
			days(StatusDone, "2026-05-18", "2026-05-19", "2026-05-20",
				"2026-05-25", "2026-05-26", "2026-05-27",
				"2026-06-08", "2026-06-09", "2026-06-10"),
			Streak{Current: 1, Best: 2, LastDay: "2026-06-08"}},
	}
	for _, c := range cases {
		if got := ComputeStreak(weekly, c.entries, today, time.Monday); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestMilestoneCrossed(t *testing.T) {
	cases := []struct{ old, new, want int }{
		{6, 7, 7}, {29, 30, 30}, {30, 31, 0}, {0, 1, 0}, {99, 100, 100}, {364, 365, 365}, {7, 6, 0},
	}
	for _, c := range cases {
		if got := MilestoneCrossed(c.old, c.new); got != c.want {
			t.Errorf("MilestoneCrossed(%d, %d) = %d, want %d", c.old, c.new, got, c.want)
		}
	}
}

func TestFreezeRules(t *testing.T) {
	if !EarnsFreeze(10, 0) || !EarnsFreeze(20, 2) {
		t.Error("10th completion under cap should earn")
	}
	if EarnsFreeze(10, 3) {
		t.Error("no earn at cap")
	}
	if EarnsFreeze(11, 0) || EarnsFreeze(0, 0) {
		t.Error("earn only on exact multiples of 10")
	}
	if !SpendsFreeze(7, 1) {
		t.Error("streak 7 with balance should spend")
	}
	if SpendsFreeze(6, 1) {
		t.Error("short streak should not spend")
	}
	if SpendsFreeze(50, 0) {
		t.Error("no balance, no spend")
	}
}
