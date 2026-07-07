// Package main is the prukka binary: one entrypoint, cobra subcommands,
// all dependency wiring.
package main

import (
	"os"
)

// Build metadata; goreleaser overrides version from the release tag.
var (
	version = "0.6.0"
	commit  = "unknown"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		// Cobra already printed the error (SilenceErrors is off).
		os.Exit(1)
	}
}
