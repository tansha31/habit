package domain

import (
	"sort"
	"time"
)

// Milestones celebrated on streaks: days for daily habits, weeks for weekly.
var Milestones = [...]int{7, 30, 100, 365}

// MilestoneCrossed returns the milestone passed moving old → new, or 0.
func MilestoneCrossed(old, new int) int {
	for _, m := range Milestones {
		if old < m && new >= m {
			return m
		}
	}
	return 0
}

// Freeze token economy (global across habits, audited in freeze_ledger).
const (
	FreezeEarnEvery = 10 // one token per N completions…
	FreezeCap       = 3  // …while balance is below the cap
	FreezeMinStreak = 7  // auto-spend only protects streaks this long
)

// EarnsFreeze reports whether the nth completion (1-based, global count)
// earns a token.
func EarnsFreeze(totalCompletions, balance int) bool {
	return totalCompletions > 0 && totalCompletions%FreezeEarnEvery == 0 && balance < FreezeCap
}

// SpendsFreeze reports whether a missed day should auto-spend a token to
// preserve the given streak.
func SpendsFreeze(streak, balance int) bool {
	return streak >= FreezeMinStreak && balance > 0
}

type Streak struct {
	Current int // days (daily) or weeks (weekly)
	Best    int
	LastDay Day // last day (daily) or week start (weekly) that kept the chain alive
}

// ComputeStreak derives a habit's streak from its full entry history.
// Rules: only done days grow the chain; freeze and pause days connect it
// without growing it; anything else (miss, skip, partial) breaks it. The
// current chain stays alive while its last day is today or yesterday —
// today being unfinished never zeroes a streak.
//
// ponytail: always a full recompute (≤366 rows per habit-year, sub-ms)
// instead of incremental cache advance — cannot desync on undo or backfill;
// go incremental only if profiling ever demands it.
func ComputeStreak(h Habit, entries []Entry, today Day, weekStart time.Weekday) Streak {
	if h.Schedule == Weekly {
		return weeklyStreak(h, entries, today, weekStart)
	}
	return dailyStreak(entries, today)
}

func dailyStreak(entries []Entry, today Day) Streak {
	byDay := make(map[Day]Status, len(entries))
	var days []Day
	for _, e := range entries {
		switch e.Status {
		case StatusDone, StatusFreeze, StatusPause:
			byDay[e.Day] = e.Status
			days = append(days, e.Day)
		}
	}
	if len(days) == 0 {
		return Streak{}
	}
	sort.Slice(days, func(i, j int) bool { return days[i] < days[j] })

	run, best := 0, 0 // run counts done days within the current unbroken chain
	for i, d := range days {
		if i > 0 && DaysBetween(days[i-1], d) != 1 {
			run = 0
		}
		if byDay[d] == StatusDone {
			run++
		}
		if run > best {
			best = run
		}
	}
	s := Streak{Best: best}
	if last := days[len(days)-1]; last == today || last == today.AddDays(-1) {
		s.Current, s.LastDay = run, last
	}
	return s
}

func weeklyStreak(h Habit, entries []Entry, today Day, weekStart time.Weekday) Streak {
	per := max(1, h.PerWeek)
	doneIn := map[Day]int{} // week start → completions
	for _, e := range entries {
		if e.Status == StatusDone {
			doneIn[e.Day.WeekStart(weekStart)]++
		}
	}
	var weeks []Day // weeks that met the target
	for w, n := range doneIn {
		if n >= per {
			weeks = append(weeks, w)
		}
	}
	if len(weeks) == 0 {
		return Streak{}
	}
	sort.Slice(weeks, func(i, j int) bool { return weeks[i] < weeks[j] })

	run, best := 0, 0
	for i, w := range weeks {
		if i > 0 && DaysBetween(weeks[i-1], w) != 7 {
			run = 0
		}
		run++
		if run > best {
			best = run
		}
	}
	s := Streak{Best: best}
	// Alive if the last qualifying week is this week or, while this week is
	// still in progress, last week.
	thisWeek := today.WeekStart(weekStart)
	if last := weeks[len(weeks)-1]; last == thisWeek || DaysBetween(last, thisWeek) == 7 {
		s.Current, s.LastDay = run, last
	}
	return s
}
