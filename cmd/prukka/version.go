package main

import (
	"runtime"

	"github.com/spf13/cobra"
)

// newVersionCmd prints build metadata (lists version as a required
// subcommand alongside the --version flag cobra already provides).
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			cmd.Printf("prukka %s\ncommit: %s\ngo: %s (%s/%s)\n",
				version, commit, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		},
	}
}
