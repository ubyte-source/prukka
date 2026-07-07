package main

import (
	"errors"
	"os"

	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/update"
)

// releasesAPI is where published builds live.
const releasesAPI = "https://api.github.com/repos/ubyte-source/prukka"

// newUpdateCmd self-updates the binary: explicit only, never
// automatic, checksum-verified against the release's checksums.txt.
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

			switch err := client.Apply(cmd.Context(), release, version, self); {
			case errors.Is(err, update.ErrUpToDate):
				cmd.Printf("prukka %s is the latest release\n", version)

				return nil
			case err != nil:
				return err
			default:
				cmd.Printf("updated %s → %s\n", version, release.Tag)

				return nil
			}
		},
	}
}
