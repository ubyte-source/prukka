package main

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
)

type setupInstallFunc func(context.Context, string, io.Writer) (string, error)

// newSetupCmd installs runtime dependencies: the pinned, checksum-verified
// static ffmpeg.
func newSetupCmd(_ *rootFlags) *cobra.Command {
	return newSetupCommand(ffmpeg.Install)
}

func newSetupCommand(install setupInstallFunc) *cobra.Command {
	var printPath bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Install the managed FFmpeg runtime dependency",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			progress := cmd.OutOrStdout()
			if printPath {
				progress = io.Discard
			}
			path, err := install(cmd.Context(), config.StateDir(), progress)
			if err != nil {
				return err
			}
			if printPath {
				// Scripts capture stdout; cobra's own Println would land
				// on stderr.
				if _, printErr := fmt.Fprintln(cmd.OutOrStdout(), path); printErr != nil {
					return printErr
				}

				return nil
			}

			cmd.Printf("ready — ffmpeg at %s\n", path)

			return nil
		},
	}
	cmd.Flags().BoolVar(&printPath, "print-path", false, "print only the resolved ffmpeg path")

	return cmd
}
