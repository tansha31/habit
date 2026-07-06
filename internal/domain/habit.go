package domain

import (
	"regexp"
	"strings"
	"time"
)

type Kind string

const (
	Check      Kind = "check"
	Quantified Kind = "quantified"
)

type Schedule string

const (
	Daily  Schedule = "daily"
	Weekly Schedule = "weekly"
)

type Status string

const (
	StatusDone    Status = "done"
	StatusPartial Status = "partial"
	StatusSkip    Status = "skip"
	StatusFreeze  Status = "freeze" // day preserved by a spent freeze token
	StatusPause   Status = "pause"  // day covered by a paused habit (no streak break)
)

var SkipReasons = []string{"tired", "travel", "sick", "other"}

type Group struct {
	ID       int64
	Name     string
	Builtin  bool
	Position int
	Reminder string // "HH:MM" default for habitd
}

type Habit struct {
	ID         int64
	Slug       string // CLI handle: `habit done meditate`
	Name       string
	Kind       Kind
	Target     float64 // quantified only
	Unit       string  // "min", "km", …
	Step       float64 // +/- increment
	Schedule   Schedule
	PerWeek    int // weekly only
	GroupID    int64
	Position   int    // order within group
	Reminder   string // "HH:MM", overrides group default
	Tags       []string
	CreatedAt  time.Time
	ArchivedAt *time.Time
	PausedAt   *time.Time
}

func (h Habit) Archived() bool { return h.ArchivedAt != nil }
func (h Habit) Paused() bool   { return h.PausedAt != nil }

// ActiveOn reports whether the habit existed and was not yet archived on d.
// ponytail: compares calendar dates of the timestamps; rollover-hour
// precision doesn't matter for a heatmap cell.
func (h Habit) ActiveOn(d Day) bool {
	if Day(h.CreatedAt.Format("2006-01-02")) > d {
		return false
	}
	return h.ArchivedAt == nil || Day(h.ArchivedAt.Format("2006-01-02")) > d
}

// StatusFor derives the entry status for a logged amount. Callers delete the
// entry instead of logging amount <= 0 on quantified habits.
func (h Habit) StatusFor(amount float64) Status {
	if h.Kind == Check || amount >= h.Target {
		return StatusDone
	}
	return StatusPartial
}

// Entry is one habit's log for one logical day.
type Entry struct {
	HabitID    int64
	Day        Day
	Status     Status
	Amount     float64 // quantified progress
	SkipReason string
	Note       string
	LoggedAt   time.Time // real wall-clock timestamp
	Source     string    // "tui" | "cli" | "freeze-auto"
}

var slugScrub = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify turns "Read fiction!" into "read-fiction" — the CLI handle.
func Slugify(name string) string {
	return strings.Trim(slugScrub.ReplaceAllString(strings.ToLower(name), "-"), "-")
}
