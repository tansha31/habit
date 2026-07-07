// Package cli implements the scriptable face of habit (spec §8): every TUI
// action as a subcommand over the same store and domain logic.
package cli

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/sahilm/fuzzy"
	"github.com/spf13/cobra"

	"habit/internal/config"
	"habit/internal/domain"
	"habit/internal/store"
	"habit/internal/ui"
)

// exitErr carries a process exit code: 2 = unknown slug (spec §8).
type exitErr struct {
	error
	code int
}

func (e exitErr) ExitCode() int { return e.code }

// Execute runs the CLI; main translates the returned error to an exit code.
func Execute() error {
	root := &cobra.Command{
		Use:           "habit",
		Short:         "A keyboard-first habit tracker for the terminal",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, cfg, err := openStoreWithConfig()
			if err != nil {
				return err
			}
			defer s.Close()
			return ui.Run(s, cfg)
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(doneCmd(), skipCmd(), undoCmd(), addCmd(), statusCmd(), exportCmd(), doctorCmd(), daemonCmd())
	return root.Execute()
}

// openStore opens the default DB with config-derived options and finalizes
// elapsed days — the same launch-time close-out the TUI and daemon run
// (spec §6.6).
func openStore() (*store.Store, error) {
	s, _, err := openStoreWithConfig()
	return s, err
}

func openStoreWithConfig() (*store.Store, config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, cfg, fmt.Errorf("config: %w", err)
	}
	s, err := store.Open(store.DefaultPath(), store.Opts{
		RolloverHour:  cfg.RolloverHour,
		WeekStart:     cfg.WeekStartDay(),
		DisableFreeze: !cfg.FreezeTokens,
	})
	if err != nil {
		return nil, cfg, err
	}
	if err := s.FinalizeThrough(s.Today()); err != nil {
		s.Close()
		return nil, cfg, err
	}
	return s, cfg, nil
}

// resolveHabit finds a habit by slug or exits 2 with a fuzzy suggestion.
func resolveHabit(s *store.Store, slug string) (*domain.Habit, error) {
	h, err := s.HabitBySlug(slug)
	if err != nil {
		return nil, err
	}
	if h != nil {
		return h, nil
	}
	habits, err := s.Habits(false)
	if err != nil {
		return nil, err
	}
	slugs := make([]string, len(habits))
	for i, hb := range habits {
		slugs[i] = hb.Slug
	}
	msg := fmt.Sprintf("unknown habit %q", slug)
	if m := fuzzy.Find(slug, slugs); len(m) > 0 {
		msg += fmt.Sprintf(" — did you mean %q?", m[0].Str)
	}
	return nil, exitErr{errors.New(msg), 2}
}

var relDay = regexp.MustCompile(`^-\d+$`)

// parseDay handles "", "YYYY-MM-DD", and "-N" (N days ago).
func parseDay(s *store.Store, flag string) (domain.Day, error) {
	switch {
	case flag == "":
		return s.Today(), nil
	case relDay.MatchString(flag):
		n, _ := strconv.Atoi(flag)
		return s.Today().AddDays(n), nil
	default:
		if _, err := time.Parse("2006-01-02", flag); err != nil {
			return "", fmt.Errorf("bad --date %q (want YYYY-MM-DD or -N)", flag)
		}
		return domain.Day(flag), nil
	}
}

// streakStr renders "31d" / "6w" for a habit's cached streak.
func streakStr(h domain.Habit, st domain.Streak) string {
	unit := "d"
	if h.Schedule == domain.Weekly {
		unit = "w"
	}
	return fmt.Sprintf("%d%s", st.Current, unit)
}

func doneCmd() *cobra.Command {
	var amount float64
	var date string
	cmd := &cobra.Command{
		Use:   "done <slug>",
		Short: "Log a completion (quantified: --amount adds to today's total)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			defer s.Close()
			h, err := resolveHabit(s, args[0])
			if err != nil {
				return err
			}
			day, err := parseDay(s, date)
			if err != nil {
				return err
			}

			e := domain.Entry{HabitID: h.ID, Day: day, Status: domain.StatusDone,
				LoggedAt: time.Now(), Source: "cli"}
			if h.Kind == domain.Quantified {
				e.Amount = h.Target // bare `done` logs the full target, like Space in the TUI
				if cmd.Flags().Changed("amount") {
					e.Amount = amount
					if prev, err := s.Entry(h.ID, day); err != nil {
						return err
					} else if prev != nil {
						e.Amount += prev.Amount // accumulate: "I did N more"
					}
				}
				e.Status = h.StatusFor(e.Amount)
			}

			before, _ := s.Streaks()
			if err := s.SetEntry(e); err != nil {
				return err
			}
			after, _ := s.Streaks()
			st := after[h.ID]

			switch e.Status {
			case domain.StatusPartial:
				fmt.Printf("◐ %s %g/%g %s\n", h.Slug, e.Amount, h.Target, h.Unit)
			default:
				line := "✓ " + h.Slug
				if h.Schedule == domain.Weekly {
					week, err := s.EntriesRange(day.WeekStart(s.Opt().WeekStart), day)
					if err != nil {
						return err
					}
					n := 0
					for _, we := range week {
						if we.HabitID == h.ID && we.Status == domain.StatusDone {
							n++
						}
					}
					line += fmt.Sprintf(" · %d/%d this wk", n, h.PerWeek)
				}
				if st.Current > 0 {
					line += " · streak " + streakStr(*h, st)
				}
				if m := domain.MilestoneCrossed(before[h.ID].Current, st.Current); m != 0 {
					line += fmt.Sprintf(" ◆ %d — milestone!", m)
				}
				fmt.Println(line)
			}
			return nil
		},
	}
	cmd.Flags().Float64Var(&amount, "amount", 0, "quantified amount to add")
	cmd.Flags().StringVar(&date, "date", "", "log for a past day: YYYY-MM-DD or -N")
	return cmd
}

func skipCmd() *cobra.Command {
	var reason, date string
	cmd := &cobra.Command{
		Use:   "skip <slug>",
		Short: "Skip today with a reason (tired · travel · sick · other · none)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ok := reason == "none"
			for _, r := range domain.SkipReasons {
				ok = ok || reason == r
			}
			if !ok {
				return fmt.Errorf("bad --reason %q (tired · travel · sick · other · none)", reason)
			}
			s, err := openStore()
			if err != nil {
				return err
			}
			defer s.Close()
			h, err := resolveHabit(s, args[0])
			if err != nil {
				return err
			}
			day, err := parseDay(s, date)
			if err != nil {
				return err
			}
			e := domain.Entry{HabitID: h.ID, Day: day, Status: domain.StatusSkip,
				LoggedAt: time.Now(), Source: "cli"}
			if reason != "none" {
				e.SkipReason = reason
			}
			if err := s.SetEntry(e); err != nil {
				return err
			}
			fmt.Printf("✕ %s (%s)\n", h.Slug, reason)
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "none", "why: tired · travel · sick · other · none")
	cmd.Flags().StringVar(&date, "date", "", "skip a past day: YYYY-MM-DD or -N")
	return cmd
}

func undoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "undo",
		Short: "Revert the last mutation (shared journal with the TUI)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			defer s.Close()
			desc, err := s.Undo()
			if err != nil {
				return err
			}
			fmt.Println("undid:", desc)
			return nil
		},
	}
}

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check database integrity and rebuild caches",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			defer s.Close()
			report, err := s.Doctor()
			if err != nil {
				return err
			}
			fmt.Println(report)
			return nil
		},
	}
}
