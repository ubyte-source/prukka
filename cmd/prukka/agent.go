package main

import (
	"errors"

	"github.com/spf13/cobra"
)

// newAgentCmd reserves the desktop-agent surface. The agent —
// per-app capture, virtual devices, caption overlay — arrives in a later release.
func newAgentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "agent",
		Short: "Run the desktop call agent (arrives in a later release)",
		RunE: func(*cobra.Command, []string) error {
			return errors.New("the desktop agent arrives in a later release — see the roadmap in README.md")
		},
	}
}
