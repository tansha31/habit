// Package daemon is habitd (spec §9): woken by launchd, it reads the
// database (read-only), finds habits whose reminder time has passed and are
// still unlogged today, and posts one notification per group per day.
// Notified-state lives in a JSON file next to the DB — zero IPC, and the
// DB stays untouched.
package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"habit/internal/config"
	"habit/internal/domain"
	"habit/internal/store"
)

type Notification struct {
	Group string
	Title string
	Body  string
}

// InQuietHours: the window may span midnight (22:00 → 08:00).
func InQuietHours(now time.Time, cfg config.Config) bool {
	from, to := cfg.QuietFrom, cfg.QuietTo
	if from == "" || to == "" || from == to {
		return false
	}
	hm := now.Format("15:04")
	if from < to {
		return hm >= from && hm < to
	}
	return hm >= from || hm < to
}

// Due returns per-group notifications for habits whose reminder has passed
// and are still unresolved today. Habit reminders override group defaults.
func Due(s *store.Store, now time.Time) ([]Notification, error) {
	snap, err := s.Snapshot()
	if err != nil {
		return nil, err
	}
	hm := now.Format("15:04")
	weekStart := snap.Today.WeekStart(s.Opt().WeekStart)

	resolved := map[int64]bool{}
	weekDone := map[int64]int{}
	for _, e := range snap.Entries {
		if e.Day == snap.Today {
			resolved[e.HabitID] = true
		}
		if e.Status == domain.StatusDone && e.Day >= weekStart {
			weekDone[e.HabitID]++
		}
	}

	var out []Notification
	for _, g := range snap.Groups {
		var due []domain.Habit
		for _, h := range snap.Habits {
			if h.GroupID != g.ID || h.Paused() || resolved[h.ID] {
				continue
			}
			if h.Schedule == domain.Weekly && weekDone[h.ID] >= h.PerWeek {
				continue
			}
			rem := h.Reminder
			if rem == "" {
				rem = g.Reminder
			}
			if rem == "" || hm < rem {
				continue
			}
			due = append(due, h)
		}
		if len(due) == 0 {
			continue
		}
		n := Notification{Group: g.Name, Title: "habit — " + g.Name}
		if len(due) == 1 {
			n.Body = habitLine(due[0], snap)
		} else {
			names := make([]string, len(due))
			for i, h := range due {
				names[i] = h.Name
			}
			n.Body = fmt.Sprintf("%d habits left: %s", len(due), strings.Join(names, ", "))
		}
		out = append(out, n)
	}
	return out, nil
}

// habitLine: "Read fiction — 12/20 min · ◆ 31-day streak on the line".
func habitLine(h domain.Habit, snap *store.Snapshot) string {
	line := h.Name
	if h.Kind == domain.Quantified {
		amount := 0.0
		for _, e := range snap.Entries {
			if e.HabitID == h.ID && e.Day == snap.Today {
				amount = e.Amount
			}
		}
		line += fmt.Sprintf(" — %g/%g %s", amount, h.Target, h.Unit)
	}
	if st := snap.Streaks[h.ID]; st.Current >= domain.FreezeMinStreak && h.Schedule == domain.Daily {
		line += fmt.Sprintf(" · ◆ %d-day streak on the line", st.Current)
	}
	return line
}

// ---- notified-state (one notification per group per day) ----

type state struct {
	Day    domain.Day `json:"day"`
	Groups []string   `json:"groups"`
}

func statePath(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "habitd-state.json")
}

func loadState(dbPath string, today domain.Day) state {
	st := state{Day: today}
	data, err := os.ReadFile(statePath(dbPath))
	if err != nil {
		return st
	}
	var prev state
	if json.Unmarshal(data, &prev) == nil && prev.Day == today {
		return prev
	}
	return st
}

func saveState(dbPath string, st state) error {
	data, _ := json.Marshal(st)
	return os.WriteFile(statePath(dbPath), data, 0o644)
}

// RunOnce is one launchd wake: quiet hours, due selection, per-day group
// dedup, post. The poster is injected so tests don't beep.
func RunOnce(s *store.Store, cfg config.Config, now time.Time, post func(Notification) error) error {
	if InQuietHours(now, cfg) {
		return nil
	}
	due, err := Due(s, now)
	if err != nil {
		return err
	}
	st := loadState(s.Path(), s.Today())
	posted := false
	for _, n := range due {
		if slices.Contains(st.Groups, n.Group) {
			continue
		}
		if err := post(n); err != nil {
			return err
		}
		st.Groups = append(st.Groups, n.Group)
		posted = true
	}
	if posted {
		return saveState(s.Path(), st)
	}
	return nil
}

// Post delivers via osascript — the unsigned-binary path (§9 note: swap
// for a signed UserNotifications helper at distribution time).
func Post(n Notification) error {
	esc := func(s string) string {
		return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
	}
	script := fmt.Sprintf(`display notification "%s" with title "%s"`, esc(n.Body), esc(n.Title))
	return exec.Command("osascript", "-e", script).Run()
}
