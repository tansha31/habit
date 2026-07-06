package cli

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/spf13/cobra"

	"habit/internal/domain"
)

var quantRe = regexp.MustCompile(`^([0-9]*\.?[0-9]+)\s*(.*)$`)

func addCmd() *cobra.Command {
	var quantified, group, reminder string
	var tags []string
	var weekly int
	var step float64
	cmd := &cobra.Command{
		Use:   `add "<name>"`,
		Short: "Create a habit non-interactively",
		Example: `  habit add "Meditate" --group morning
  habit add "Read fiction" --quantified 20min --step 5 --group afternoon --tag reading
  habit add "Gym" --weekly 3 --group afternoon`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			defer s.Close()

			h := domain.Habit{Name: args[0], Kind: domain.Check,
				Schedule: domain.Daily, Step: step, Reminder: reminder, Tags: tags}
			if quantified != "" {
				m := quantRe.FindStringSubmatch(quantified)
				if m == nil {
					return fmt.Errorf("bad --quantified %q (want e.g. 20min, 5km)", quantified)
				}
				h.Kind = domain.Quantified
				h.Target, _ = strconv.ParseFloat(m[1], 64)
				h.Unit = m[2]
			}
			if weekly > 0 {
				h.Schedule, h.PerWeek = domain.Weekly, weekly
			}
			g, err := s.EnsureGroup(group)
			if err != nil {
				return err
			}
			h.GroupID = g.ID
			if err := s.CreateHabit(&h); err != nil {
				return err
			}
			fmt.Printf("added %s (%s, %s)\n", h.Slug, h.Kind, g.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&quantified, "quantified", "", "target with unit, e.g. 20min")
	cmd.Flags().IntVar(&weekly, "weekly", 0, "N times per week instead of daily")
	cmd.Flags().StringVar(&group, "group", "Morning", "group name (created if new)")
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "tag (repeatable)")
	cmd.Flags().StringVar(&reminder, "reminder", "", "reminder time HH:MM (needs habitd)")
	cmd.Flags().Float64Var(&step, "step", 1, "+/- increment for quantified habits")
	return cmd
}
