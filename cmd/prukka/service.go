package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/control/service"
)

// newServiceCmd manages the per-user service wrapping the daemon.
func newServiceCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Install, remove or inspect the service running the daemon",
	}

	cmd.AddCommand(newServiceInstallCmd(flags), newServiceRemoveCmd(), newServiceRestartCmd(), newServiceStatusCmd())

	return cmd
}

// newServiceInstallCmd installs (and optionally starts) the service.
func newServiceInstallCmd(flags *rootFlags) *cobra.Command {
	var (
		now       bool
		printOnly bool
	)

	cmd := &cobra.Command{
		Use:   verbInstall,
		Short: "Install the daemon as a per-user service",
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

			if note := lingerNote(); note != "" {
				cmd.Println(note)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&now, "now", false, "start the service immediately")
	cmd.Flags().BoolVar(&printOnly, "print", false, "print the service definition instead of installing")

	return cmd
}

// lingerNote surfaces the systemd lingering requirement on Linux: without
// it the per-user daemon stops at logout (an ssh session included) and
// never starts at boot. logind records lingering as a file per user; on a
// systemd-booted machine its absence means the note is needed.
func lingerNote() string {
	if runtime.GOOS != "linux" {
		return ""
	}

	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return "" // not a systemd machine; the unit install already failed loudly
	}

	me, err := user.Current()
	if err != nil {
		return ""
	}

	if _, err := os.Stat("/var/lib/systemd/linger/" + me.Username); err == nil {
		return "" // lingering already enabled
	}

	return "note: the service stops when you log out — keep it running with: loginctl enable-linger"
}

// startedSuffix words the install confirmation.
func startedSuffix(now bool) string {
	if now {
		return " and started"
	}

	return " (starts at next login, or run `prukka service install --now`)"
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
		Use:   verbRemove,
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

// newServiceRestartCmd relaunches the service.
func newServiceRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "Restart the daemon service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := service.Restart(cmd.Context()); err != nil {
				return fmt.Errorf("restart service: %w", err)
			}

			cmd.Println("service restarted")

			return nil
		},
	}
}

// newServiceStatusCmd reports the service state.
func newServiceStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   verbStatus,
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
