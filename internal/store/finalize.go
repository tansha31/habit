package store

import (
	"sort"
	"time"

	"habit/internal/domain"
)

// FinalizeThrough closes out every logical day before today, exactly once
// (spec §6.6): paused habits get pause entries (streak connectors), and a
// missed day on a streak ≥ 7 auto-spends a freeze token if one is banked —
// longest streak first when tokens run short. Weekly habits have no per-day
// finalization. System behavior, deliberately NOT journaled: `u` must never
// revert an auto-freeze.
//
// Runs at launch and on the rollover tick; both TUI and CLI call it, so the
// logic lives here once.
func (s *Store) FinalizeThrough(today domain.Day) error {
	last := domain.Day(metaGet(s.db, "last_finalized"))
	if last == "" {
		// First run: nothing before today can owe anything yet.
		return metaSet(s.db, "last_finalized", string(today.AddDays(-1)))
	}
	if last >= today.AddDays(-1) {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	habits, err := habitsQ(tx, false)
	if err != nil {
		return err
	}
	balance, err := freezeBalanceQ(tx)
	if err != nil {
		return err
	}
	entries := map[int64][]domain.Entry{} // per habit, ordered by day
	byDay := map[int64]map[domain.Day]bool{}
	for _, h := range habits {
		if h.Schedule != domain.Daily {
			continue
		}
		es, err := entriesForQ(tx, h.ID)
		if err != nil {
			return err
		}
		entries[h.ID] = es
		byDay[h.ID] = map[domain.Day]bool{}
		for _, e := range es {
			byDay[h.ID][e.Day] = true
		}
	}

	insert := func(e domain.Entry) error {
		_, err := tx.Exec(`INSERT OR REPLACE INTO entry (`+entryCols+`) VALUES (?,?,?,?,?,?,?,?)`,
			e.HabitID, e.Day, e.Status, e.Amount, e.SkipReason, e.Note, fmtTime(e.LoggedAt), e.Source)
		entries[e.HabitID] = append(entries[e.HabitID], e)
		byDay[e.HabitID][e.Day] = true
		return err
	}

	for d := last.AddDays(1); d < today; d = d.AddDays(1) {
		type miss struct {
			habitID int64
			streak  int
		}
		var misses []miss
		for _, h := range habits {
			if h.Schedule != domain.Daily || !h.ActiveOn(d) || byDay[h.ID][d] {
				continue
			}
			if h.Paused() {
				if err := insert(domain.Entry{HabitID: h.ID, Day: d,
					Status: domain.StatusPause, LoggedAt: time.Now(), Source: "auto"}); err != nil {
					return err
				}
				continue
			}
			// Streak with a chain ending at d-1, i.e. what's on the line.
			cur := domain.ComputeStreak(h, entries[h.ID], d, s.opt.WeekStart).Current
			if cur > 0 {
				misses = append(misses, miss{h.ID, cur})
			}
		}
		sort.Slice(misses, func(i, j int) bool { return misses[i].streak > misses[j].streak })
		for _, m := range misses {
			if s.opt.DisableFreeze || !domain.SpendsFreeze(m.streak, balance) {
				continue
			}
			if err := insert(domain.Entry{HabitID: m.habitID, Day: d,
				Status: domain.StatusFreeze, LoggedAt: time.Now(), Source: "freeze-auto"}); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO freeze_ledger (day, delta, habit_id, reason) VALUES (?, -1, ?, 'auto-spend')`,
				d, m.habitID); err != nil {
				return err
			}
			balance--
		}
	}

	// Refresh every cache, not just touched habits: Current is relative to
	// "today", so chains that silently died and weekly streaks whose week
	// rolled over are stale now too.
	for _, h := range habits {
		if err := recomputeStreak(tx, h.ID, today, s.opt.WeekStart); err != nil {
			return err
		}
	}
	if err := metaSet(tx, "last_finalized", string(today.AddDays(-1))); err != nil {
		return err
	}
	return tx.Commit()
}
