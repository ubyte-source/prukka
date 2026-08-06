package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/devices"
	"github.com/ubyte-source/prukka/internal/osservice"
	"github.com/ubyte-source/prukka/internal/update"
)

// releasesAPI is where published builds live.
const releasesAPI = "https://api.github.com/repos/ubyte-source/prukka"

// newUpdateCmd self-updates the binary, checksum-verified against the
// release's checksums.txt.
func newUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update prukka to the latest release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client := update.New(releasesAPI)

			release, err := client.Latest(cmd.Context())
			if err != nil {
				return err
			}

			self, err := os.Executable()
			if err != nil {
				return err
			}

			// The install may be reached through a symlink; replacing the link
			// would leave the service on the old binary forever.
			if resolved, resolveErr := filepath.EvalSymlinks(self); resolveErr == nil {
				self = resolved
			}

			switch err := client.Apply(cmd.Context(), release, version, self); {
			case errors.Is(err, update.ErrUpToDate):
				cmd.Printf("prukka %s is the latest release\n", version)

				return nil
			case err != nil:
				return err
			default:
				cmd.Printf("updated %s → %s%s%s\n",
					version, release.Tag, restartedNote(cmd.Context()), devicesNote(cmd.Context()))

				return nil
			}
		},
	}
}

// restartedNote relaunches a running daemon and words the outcome.
func restartedNote(ctx context.Context) string {
	state, err := osservice.Status(ctx)
	if err != nil {
		// `sudo prukka update` cannot see the per-user service, so an unknown
		// state must never read as success.
		return "\ncould not check the daemon (" + err.Error() + ") — if it runs, restart it: " + osservice.RestartHint()
	}

	if !osservice.Running(state) {
		return ""
	}

	if restartErr := osservice.Restart(ctx); restartErr != nil {
		return "\ndaemon still runs the old version — run: " + osservice.RestartHint()
	}

	return " (daemon restarted)"
}

// devicesNote refreshes previously installed device drivers alongside the
// binary.
func devicesNote(ctx context.Context) string {
	if !devices.Recorded() {
		return ""
	}

	if _, err := devices.Install(ctx); err != nil {
		return "\ndevice drivers may be outdated — run: " + devices.InstallHint()
	}

	return " (device drivers refreshed)"
}
