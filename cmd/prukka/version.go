package main

import (
	"runtime"

	"github.com/spf13/cobra"
)

// newVersionCmd prints build metadata.
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
