package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/control/service"
)

// newServiceCmd manages the OS service wrapping the daemon.
func newServiceCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Install, remove or inspect the OS service running the daemon",
	}

	cmd.AddCommand(newServiceInstallCmd(flags), newServiceRemoveCmd(), newServiceStatusCmd())

	return cmd
}

// newServiceInstallCmd installs (and optionally starts) the service.
func newServiceInstallCmd(flags *rootFlags) *cobra.Command {
	var (
		now       bool
		printOnly bool
	)

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the daemon as a system service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := serviceOptions(flags, now)
			if err != nil {
				return err
			}

			if printOnly {
				path, content, renderErr := service.Rendered(opts)
				if renderErr != nil {
					return renderErr
				}

				cmd.Printf("# %s\n%s", path, content)

				return nil
			}

			if installErr := service.Install(cmd.Context(), opts); installErr != nil {
				return installErr
			}

			cmd.Println("service installed" + startedSuffix(now))

			return nil
		},
	}

	cmd.Flags().BoolVar(&now, "now", false, "start the service immediately")
	cmd.Flags().BoolVar(&printOnly, "print", false, "print the service definition instead of installing")

	return cmd
}

// startedSuffix words the install confirmation.
func startedSuffix(now bool) string {
	if now {
		return " and started"
	}

	return " (start at next boot, or run `prukka service install --now`)"
}

// serviceOptions resolves the binary and config paths for installation.
func serviceOptions(flags *rootFlags, now bool) (*service.Options, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate prukka binary: %w", err)
	}

	exe, err = filepath.Abs(exe)
	if err != nil {
		return nil, fmt.Errorf("resolve binary path: %w", err)
	}

	return &service.Options{ExecPath: exe, ConfigPath: flags.config, Now: now}, nil
}

// newServiceRemoveCmd removes the service.
func newServiceRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove",
		Short: "Stop and remove the daemon service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := service.Remove(cmd.Context()); err != nil {
				return err
			}

			cmd.Println("service removed")

			return nil
		},
	}
}

// newServiceStatusCmd reports the service state.
func newServiceStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the daemon service state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			state, err := service.Status(cmd.Context())
			if err != nil {
				return err
			}

			cmd.Println(state)

			return nil
		},
	}
}
