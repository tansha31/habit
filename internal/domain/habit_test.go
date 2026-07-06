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
	ts := func(s string) time.Time {
		tm, _ := time.Parse("2006-01-02", s)
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
