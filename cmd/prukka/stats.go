package main

import (
	"context"
	"time"

	"github.com/spf13/cobra"
	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

// watchInterval paces `stats --watch` refreshes.
const watchInterval = 2 * time.Second

// newStatsCmd prints daemon counters, optionally refreshing.
func newStatsCmd(flags *rootFlags) *cobra.Command {
	var watch bool

	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show daemon statistics (sessions and uptime)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := printStats(cmd, flags); err != nil {
				return err
			}

			if !watch {
				return nil
			}

			ticker := time.NewTicker(watchInterval)
			defer ticker.Stop()

			for {
				select {
				case <-cmd.Context().Done():
					return nil
				case <-ticker.C:
					if err := printStats(cmd, flags); err != nil {
						return err
					}
				}
			}
		},
	}

	cmd.Flags().BoolVar(&watch, "watch", false, "refresh continuously")

	return cmd
}

// printStats fetches and renders one stats snapshot.
func printStats(cmd *cobra.Command, flags *rootFlags) error {
	return withControl(cmd, flags, func(ctx context.Context, client v1.ControlClient) error {
		resp, err := client.Stats(ctx, &v1.StatsRequest{})
		if err != nil {
			return err
		}

		uptime := time.Duration(resp.GetUptimeSeconds() * float64(time.Second)).Round(time.Second)
		cmd.Printf("sessions: %d · uptime: %s · daemon: %s\n",
			resp.GetSessionsActive(), uptime, resp.GetVersion())

		return nil
	})
}
