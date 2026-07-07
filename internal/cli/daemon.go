package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"habit/internal/config"
	"habit/internal/daemon"
	"habit/internal/store"
)

func daemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage habitd, the reminder agent (off by default)",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "run",
			Short: "One reminder pass (what launchd invokes)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg, err := config.Load()
				if err != nil {
					return err
				}
				s, err := store.OpenRO(store.DefaultPath(), store.Opts{
					RolloverHour: cfg.RolloverHour, WeekStart: cfg.WeekStartDay()})
				if os.IsNotExist(err) {
					return nil // no database yet: nothing to remind about
				}
				if err != nil {
					return err
				}
				defer s.Close()
				return daemon.RunOnce(s, cfg, time.Now(), daemon.Post)
			},
		},
		&cobra.Command{
			Use:   "install",
			Short: "Install and load the launchd agent",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := daemon.Install(); err != nil {
					return err
				}
				fmt.Println("habitd installed:", daemon.PlistPath())
				return nil
			},
		},
		&cobra.Command{
			Use:   "remove",
			Short: "Unload and remove the launchd agent",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := daemon.Remove(); err != nil {
					return err
				}
				fmt.Println("habitd removed")
				return nil
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Agent status",
			Args:  cobra.NoArgs,
			Run: func(cmd *cobra.Command, args []string) {
				fmt.Println(daemon.Status())
			},
		},
	)
	return cmd
}
