package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"habit/internal/domain"
	"habit/internal/store"
)

// dayRow is one habit's state today, shared by all status output modes.
type dayRow struct {
	Slug     string  `json:"slug"`
	Name     string  `json:"name"`
	Group    string  `json:"group"`
	Status   string  `json:"status"` // done|partial|skip|freeze|pending|paused|week-progress
	Amount   float64 `json:"amount,omitempty"`
	Target   float64 `json:"target,omitempty"`
	Unit     string  `json:"unit,omitempty"`
	WeekDone int     `json:"week_done,omitempty"`
	PerWeek  int     `json:"per_week,omitempty"`
	Streak   int     `json:"streak"`
	StreakU  string  `json:"streak_unit"` // "d" | "w"
	Best     int     `json:"best"`
}

type dayStatus struct {
	Date   domain.Day `json:"date"`
	Done   int        `json:"done"`
	Total  int        `json:"total"`
	Freeze int        `json:"freeze"`
	Habits []dayRow   `json:"habits"`
}

func buildStatus(snap *store.Snapshot, weekStart domain.Day) dayStatus {
	st := dayStatus{Date: snap.Today, Freeze: snap.Freeze}
	groups := map[int64]string{}
	for _, g := range snap.Groups {
		groups[g.ID] = g.Name
	}
	todays := map[int64]domain.Entry{}
	weekDone := map[int64]int{}
	for _, e := range snap.Entries {
		if e.Day == snap.Today {
			todays[e.HabitID] = e
		}
		if e.Status == domain.StatusDone && e.Day >= weekStart {
			weekDone[e.HabitID]++
		}
	}
	for _, h := range snap.Habits {
		cache := snap.Streaks[h.ID]
		row := dayRow{Slug: h.Slug, Name: h.Name, Group: groups[h.GroupID],
			Target: h.Target, Unit: h.Unit, Streak: cache.Current, Best: cache.Best, StreakU: "d"}
		if h.Schedule == domain.Weekly {
			row.StreakU, row.WeekDone, row.PerWeek = "w", weekDone[h.ID], h.PerWeek
		}
		e, logged := todays[h.ID]
		switch {
		case h.Paused():
			row.Status = "paused"
		case logged:
			row.Status = string(e.Status)
			row.Amount = e.Amount
		case h.Schedule == domain.Weekly && weekDone[h.ID] > 0:
			row.Status = "week-progress"
		default:
			row.Status = "pending"
		}
		if row.Status != "paused" {
			st.Total++
			if row.Status == "done" {
				st.Done++
			}
		}
		st.Habits = append(st.Habits, row)
	}
	return st
}

var statusGlyph = map[string]string{
	"done": "✓", "partial": "◐", "skip": "✕", "freeze": "❄",
	"pending": "○", "paused": "‖", "week-progress": "●", "pause": "‖",
}

func statusCmd() *cobra.Command {
	var asJSON, asPrompt bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Today's summary (--json for scripts, --prompt for shell prompts)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			defer s.Close()
			snap, err := s.Snapshot()
			if err != nil {
				return err
			}
			st := buildStatus(snap, snap.Today.WeekStart(s.Opt().WeekStart))

			switch {
			case asPrompt:
				// Spec §8: `⬢ 6/9 ◆47` — day fraction + top live streak.
				top := 0
				for _, r := range st.Habits {
					if r.StreakU == "d" && r.Streak > top {
						top = r.Streak
					}
				}
				out := fmt.Sprintf("⬢ %d/%d", st.Done, st.Total)
				if top > 0 {
					out += fmt.Sprintf(" ◆%d", top)
				}
				fmt.Println(out)
			case asJSON:
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(st)
			default:
				printHuman(st)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "stable-schema JSON")
	cmd.Flags().BoolVar(&asPrompt, "prompt", false, "one-line prompt segment")
	return cmd
}

func printHuman(st dayStatus) {
	fmt.Printf("%s · %d/%d done", st.Date, st.Done, st.Total)
	if st.Freeze > 0 {
		fmt.Printf(" · ❄ %d", st.Freeze)
	}
	fmt.Println()
	group := ""
	for _, r := range st.Habits {
		if r.Group != group {
			group = r.Group
			fmt.Printf("\n%s\n", strings.ToUpper(group))
		}
		meta := ""
		switch {
		case r.PerWeek > 0:
			meta = fmt.Sprintf("%d/%d this wk", r.WeekDone, r.PerWeek)
		case r.Target > 0:
			meta = fmt.Sprintf("%g/%g %s", r.Amount, r.Target, r.Unit)
		}
		streak := ""
		if r.Streak > 0 {
			streak = fmt.Sprintf("%d%s", r.Streak, r.StreakU)
		}
		fmt.Printf("  %s %-20s %-14s %s\n", statusGlyph[r.Status], r.Name, meta, streak)
	}
}
