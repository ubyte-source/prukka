package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/control"
	"github.com/ubyte-source/prukka/internal/core/config"
	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
	"github.com/ubyte-source/prukka/internal/tray"
)

// newTrayCmd runs the system tray companion. The daemon runs
// separately; the tray is a thin client over the control plane.
func newTrayCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "tray",
		Short: "Run the system tray companion for a local daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, log, err := flags.load()
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			tray.Run(ctx, &tray.Config{
				Stats:        &controlStats{cfg: cfg, log: log},
				Log:          log,
				DashboardURL: dashboardURL("http://" + cfg.Daemon.HTTP),
			})

			return nil
		},
	}
}

// controlStats adapts the control plane to the tray's StatsSource port.
// Dialing per poll keeps the tray resilient to daemon restarts.
type controlStats struct {
	cfg *config.Config
	log *slog.Logger
}

// Stats implements tray.StatsSource.
func (c *controlStats) Stats(ctx context.Context) (tray.Stats, error) {
	conn, err := control.Dial(c.cfg)
	if err != nil {
		return tray.Stats{}, err
	}

	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			c.log.Debug("closing control connection", "err", closeErr)
		}
	}()

	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	resp, statsErr := v1.NewControlClient(conn).Stats(callCtx, &v1.StatsRequest{})
	if statsErr != nil {
		return tray.Stats{}, fmt.Errorf("query stats: %w", statsErr)
	}

	if resp == nil {
		return tray.Stats{}, errors.New("empty stats response")
	}

	return tray.Stats{
		Sessions:       int(resp.GetSessionsActive()),
		CostEURPerHour: resp.GetCostEurPerHour(),
		Version:        resp.GetVersion(),
	}, nil
}
