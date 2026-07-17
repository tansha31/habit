package domain

import (
	"testing"
	"time"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Meditate":         "meditate",
		"Read fiction!":    "read-fiction",
		"No screens 22:00": "no-screens-22-00",
		"  Cold  Shower  ": "cold-shower",
		"Café ☕":           "caf",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStatusFor(t *testing.T) {
	check := Habit{Kind: Check}
	quant := Habit{Kind: Quantified, Target: 20}
	if got := check.StatusFor(0); got != StatusDone {
		t.Errorf("check habit: %s", got)
	}
	if got := quant.StatusFor(20); got != StatusDone {
		t.Errorf("at target: %s", got)
	}
	if got := quant.StatusFor(25); got != StatusDone {
		t.Errorf("over target: %s", got)
	}
	if got := quant.StatusFor(12); got != StatusPartial {
		t.Errorf("below target: %s", got)
	}
}

func TestActiveOn(t *testing.T) {
	// Local times: creation/archive days are local calendar days.
	ts := func(s string) time.Time {
		tm, _ := time.ParseInLocation("2006-01-02", s, time.Local)
		return tm
	}
	created := ts("2026-06-01")
	archived := ts("2026-07-01")
	h := Habit{CreatedAt: created}
	if h.ActiveOn("2026-05-31") {
		t.Error("active before creation")
	}
	if !h.ActiveOn("2026-06-01") {
		t.Error("inactive on creation day")
	}
	h.ArchivedAt = &archived
	if h.ActiveOn("2026-07-01") {
		t.Error("active on archive day")
	}
	if !h.ActiveOn("2026-06-30") {
		t.Error("inactive before archive")
	}
}

// CreatedAt is stored UTC; an evening creation west of UTC lands on the next
// UTC calendar date, and CreatedDay/ActiveOn must still report the local day.
func TestCreatedDayUsesLocalDate(t *testing.T) {
	oldLocal := time.Local
	time.Local = time.FixedZone("UTC-5", -5*3600)
	defer func() { time.Local = oldLocal }()

	// 20:00 local Jul 12 == 01:00 UTC Jul 13.
	created := time.Date(2026, 7, 13, 1, 0, 0, 0, time.UTC)
	h := Habit{CreatedAt: created}
	if got := h.CreatedDay(); got != "2026-07-12" {
		t.Fatalf("CreatedDay = %s, want 2026-07-12", got)
	}
	if !h.ActiveOn("2026-07-12") {
		t.Error("inactive on its local creation day")
	}
}
