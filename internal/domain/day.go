package domain

import "time"

// Day is a logical day key, "YYYY-MM-DD". Logical days run from rollover
// hour to rollover hour (default 03:00), not midnight to midnight. Applying
// the rollover rule once at write time (here) makes every read a plain
// string comparison or range scan.
type Day string

// LogicalDay maps a wall-clock instant to its logical day: times before
// rolloverHour belong to the previous calendar date.
func LogicalDay(now time.Time, rolloverHour int) Day {
	if now.Hour() < rolloverHour {
		now = now.AddDate(0, 0, -1)
	}
	return Day(now.Format("2006-01-02"))
}

// Time returns the day at midnight UTC — for date arithmetic only.
func (d Day) Time() time.Time {
	t, _ := time.Parse("2006-01-02", string(d)) // ponytail: invalid Day → zero time
	return t
}

func (d Day) AddDays(n int) Day {
	return Day(d.Time().AddDate(0, 0, n).Format("2006-01-02"))
}

func (d Day) Weekday() time.Weekday { return d.Time().Weekday() }

// WeekStart returns the first day of the week containing d.
func (d Day) WeekStart(start time.Weekday) Day {
	return d.AddDays(-((int(d.Weekday()) - int(start) + 7) % 7))
}

// DaysBetween returns b minus a in days.
func DaysBetween(a, b Day) int {
	return int(b.Time().Sub(a.Time()) / (24 * time.Hour))
}
