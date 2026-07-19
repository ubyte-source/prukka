package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/devices"
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
		Short: "Check the environment: config, speech engine, ffmpeg, state, daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, _, err := flags.load()
			if err != nil {
				return err
			}

			checks := doctor.Run(cfg)
			checks = append(checks, devicesCheck(cmd.Context()), daemonCheck(cmd, flags))

			cmd.Printf("config: %s (state: %s)\n\n", configSource(flags), config.StateDir())

			failed := 0

			for _, c := range checks {
				cmd.Printf("%s %-16s %s\n", statusGlyphs[c.Status], c.Name, c.Detail)

				if c.Status == doctor.StatusFail {
					failed++
				}
			}

			if failed > 0 {
				return fmt.Errorf("%d check(s) failed", failed)
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

// devicesCheck summarizes the virtual devices from their install
// records; a pending or stale driver names its one next step.
func devicesCheck(ctx context.Context) doctor.Check {
	results, err := devices.Status(ctx)
	if err != nil {
		return doctor.Check{Name: devicesName, Status: doctor.StatusWarn, Detail: err.Error()}
	}

	return deviceVerdict(results)
}

// deviceVerdict folds the device states into one check line.
func deviceVerdict(results []devices.Result) doctor.Check {
	installed := 0

	for _, result := range results {
		if result.State == devices.StateInstalled {
			installed++
		}
	}

	// A fresh machine gets the one install command before any per-device
	// note: on Windows the audio drivers always read manual, and that
	// note must not bury the actual first step.
	if installed == 0 {
		return doctor.Check{
			Name:   devicesName,
			Status: doctor.StatusWarn,
			Detail: "virtual devices not installed — run: " + devices.InstallHint(),
		}
	}

	for _, result := range results {
		if detail := deviceAttention(result); detail != "" {
			return doctor.Check{Name: devicesName, Status: doctor.StatusWarn, Detail: detail}
		}
	}

	return doctor.Check{
		Name:   devicesName,
		Status: doctor.StatusOK,
		Detail: fmt.Sprintf("%d of %d installed", installed, len(results)),
	}
}

// deviceAttention words the warning a device state calls for; an empty
// string means nothing to do.
func deviceAttention(result devices.Result) string {
	switch result.State {
	case devices.StateOutdated:
		return string(result.Kind) + " driver is outdated — run: " + devices.InstallHint()
	case devices.StateManual:
		return string(result.Kind) + ": " + result.NextStep
	default:
		return ""
	}
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
