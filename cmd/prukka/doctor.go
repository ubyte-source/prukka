package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/doctor"

	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

// statusGlyphs maps probe outcomes to their terminal rendering.
var statusGlyphs = map[doctor.Status]string{
	doctor.StatusOK:   "✓",
	doctor.StatusWarn: "!",
	doctor.StatusFail: "✗",
}

// newDoctorCmd runs environment checks locally and, when a daemon is up,
// reports its health too.
func newDoctorCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check the environment: ffmpeg, keychain, keys, state, daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, _, err := flags.load()
			if err != nil {
				return err
			}

			checks := doctor.Run(cfg)
			checks = append(checks, daemonCheck(cmd, flags))

			cmd.Printf("config: %s (state: %s)\n\n", configSource(flags), config.StateDir())

			failed := false

			for _, c := range checks {
				cmd.Printf("%s %-16s %s\n", statusGlyphs[c.Status], c.Name, c.Detail)

				if c.Status == doctor.StatusFail {
					failed = true
				}
			}

			if failed {
				return fmt.Errorf("%d check(s) failed", countFailed(checks))
			}

			return nil
		},
	}
}

// configSource names the config file in effect.
func configSource(flags *rootFlags) string {
	if flags.config != "" {
		return flags.config
	}

	return config.DefaultPath() + " (or built-in defaults)"
}

// countFailed tallies failing checks.
func countFailed(checks []doctor.Check) int {
	n := 0

	for _, c := range checks {
		if c.Status == doctor.StatusFail {
			n++
		}
	}

	return n
}

// daemonCheck probes the local daemon over the control plane.
func daemonCheck(cmd *cobra.Command, flags *rootFlags) doctor.Check {
	var detail string

	err := withControl(cmd, flags, func(ctx context.Context, client v1.ControlClient) error {
		resp, statsErr := client.Stats(ctx, &v1.StatsRequest{})
		if statsErr != nil {
			return statsErr
		}

		detail = fmt.Sprintf("running, version %s, %d session(s)", resp.GetVersion(), resp.GetSessionsActive())

		return nil
	})
	if err != nil {
		return doctor.Check{
			Name:   daemonName,
			Status: doctor.StatusWarn,
			Detail: fmt.Sprintf("not reachable (%v) — start it with `prukka up` or `prukka service install --now`", err),
		}
	}

	return doctor.Check{Name: daemonName, Status: doctor.StatusOK, Detail: detail}
}
