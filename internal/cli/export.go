package cli

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"habit/internal/domain"
)

func exportCmd() *cobra.Command {
	var format, from, to string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Full-fidelity data export to stdout",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			defer s.Close()
			lo, hi := domain.Day("0000-01-01"), domain.Day("9999-12-31")
			if from != "" {
				if lo, err = parseDay(s, from); err != nil {
					return err
				}
			}
			if to != "" {
				if hi, err = parseDay(s, to); err != nil {
					return err
				}
			}
			habits, err := s.Habits(true)
			if err != nil {
				return err
			}
			entries, err := s.EntriesRange(lo, hi)
			if err != nil {
				return err
			}

			switch format {
			case "json":
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{"habits": habits, "entries": entries})
			case "csv":
				slugs := map[int64]string{}
				for _, h := range habits {
					slugs[h.ID] = h.Slug
				}
				w := csv.NewWriter(os.Stdout)
				w.Write([]string{"slug", "day", "status", "amount", "skip_reason", "note", "logged_at", "source"})
				for _, e := range entries {
					w.Write([]string{slugs[e.HabitID], string(e.Day), string(e.Status),
						fmt.Sprintf("%g", e.Amount), e.SkipReason, e.Note,
						e.LoggedAt.Format("2006-01-02T15:04:05Z07:00"), e.Source})
				}
				w.Flush()
				return w.Error()
			default:
				return fmt.Errorf("bad --format %q (json or csv)", format)
			}
		},
	}
	cmd.Flags().StringVar(&format, "format", "json", "json or csv")
	cmd.Flags().StringVar(&from, "from", "", "start day (YYYY-MM-DD or -N)")
	cmd.Flags().StringVar(&to, "to", "", "end day (YYYY-MM-DD or -N)")
	return cmd
}
