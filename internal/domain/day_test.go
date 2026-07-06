package domain

import (
	"testing"
	"time"
)

func at(t *testing.T, loc *time.Location, s string) time.Time {
	t.Helper()
	tm, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func TestLogicalDay(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		clock    string
		rollover int
		want     Day
	}{
		{"2026-07-07 02:59", 3, "2026-07-06"}, // before rollover → previous day
		{"2026-07-07 03:00", 3, "2026-07-07"}, // at rollover → new day
		{"2026-07-07 16:40", 3, "2026-07-07"},
		{"2026-07-07 00:30", 0, "2026-07-07"}, // midnight rollover: calendar day
		{"2026-01-01 00:30", 3, "2025-12-31"}, // across year boundary
		{"2026-03-08 01:30", 3, "2026-03-07"}, // DST spring-forward night (2 AM skipped)
		{"2026-03-08 03:30", 3, "2026-03-08"},
		{"2026-11-01 01:30", 3, "2026-10-31"}, // DST fall-back night
	}
	for _, c := range cases {
		if got := LogicalDay(at(t, ny, c.clock), c.rollover); got != c.want {
			t.Errorf("LogicalDay(%s, %d) = %s, want %s", c.clock, c.rollover, got, c.want)
		}
	}
}

func TestDayArithmetic(t *testing.T) {
	if got := Day("2026-01-31").AddDays(1); got != "2026-02-01" {
		t.Errorf("AddDays month boundary: %s", got)
	}
	if got := Day("2025-12-31").AddDays(1); got != "2026-01-01" {
		t.Errorf("AddDays year boundary: %s", got)
	}
	if got := Day("2026-07-07").AddDays(-7); got != "2026-06-30" {
		t.Errorf("AddDays negative: %s", got)
	}
	if got := DaysBetween("2026-06-30", "2026-07-07"); got != 7 {
		t.Errorf("DaysBetween = %d, want 7", got)
	}
	if got := Day("2026-07-07").Weekday(); got != time.Tuesday {
		t.Errorf("Weekday = %s, want Tuesday", got)
	}
}

func TestWeekStart(t *testing.T) {
	cases := []struct {
		day   Day
		start time.Weekday
		want  Day
	}{
		{"2026-07-07", time.Monday, "2026-07-06"}, // Tue → previous Mon
		{"2026-07-06", time.Monday, "2026-07-06"}, // Mon → itself
		{"2026-07-05", time.Monday, "2026-06-29"}, // Sun belongs to Mon-start week
		{"2026-07-07", time.Sunday, "2026-07-05"}, // Tue → previous Sun
	}
	for _, c := range cases {
		if got := c.day.WeekStart(c.start); got != c.want {
			t.Errorf("WeekStart(%s, %s) = %s, want %s", c.day, c.start, got, c.want)
		}
	}
}
