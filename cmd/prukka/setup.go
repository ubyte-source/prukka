package main

import (
	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
)

// newSetupCmd installs runtime dependencies: the pinned, checksum-verified
// static ffmpeg.
func newSetupCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Install runtime dependencies (ffmpeg) automatically",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, _, err := flags.load(); err != nil {
				return err
			}

			path, err := ffmpeg.Install(cmd.Context(), config.StateDir(), cmd.OutOrStdout())
			if err != nil {
				return err
			}

			cmd.Printf("ready — ffmpeg at %s\n", path)

			return nil
		},
	}
}
