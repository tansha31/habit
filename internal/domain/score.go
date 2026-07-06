package domain

// EntryScore is one habit's completion contribution: done = 1, partial =
// amount/target clamped to 1; skip, freeze and anything else = 0. Freeze
// preserves the streak, not the score — the heatmap stays honest.
func EntryScore(h Habit, e Entry) float64 {
	switch e.Status {
	case StatusDone:
		return 1
	case StatusPartial:
		if h.Target > 0 {
			return min(1, e.Amount/h.Target)
		}
	}
	return 0
}

// DayScore is the aggregate heatmap score for one day: the mean completion
// across habits scheduled that day. Daily habits count whether or not they
// were logged; weekly and paused habits count only on days they logged
// (a weekly habit isn't "due" on any particular day).
func DayScore(habits []Habit, entries []Entry, day Day) float64 {
	byHabit := make(map[int64]Entry, len(entries))
	for _, e := range entries {
		if e.Day == day {
			byHabit[e.HabitID] = e
		}
	}
	var sum float64
	n := 0
	for _, h := range habits {
		e, logged := byHabit[h.ID]
		if !h.ActiveOn(day) || (logged && e.Status == StatusPause) {
			continue
		}
		if !logged && (h.Schedule == Weekly || h.Paused()) {
			continue
		}
		n++
		sum += EntryScore(h, e)
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}
