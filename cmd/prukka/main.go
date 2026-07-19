// Package main is the prukka binary: one entrypoint, cobra subcommands,
// all dependency wiring.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// Build metadata; goreleaser overrides version from the release tag.
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		// Cobra already printed the error (SilenceErrors is off).
		return 1
	}

	return 0
}
