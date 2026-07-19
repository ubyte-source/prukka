package main

import (
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/devices"
)

// devicesName is the command and doctor-check identity of the virtual
// devices.
const devicesName = "devices"

// The management verbs shared by the service and devices commands.
const (
	verbInstall = "install"
	verbRemove  = "remove"
	verbStatus  = "status"
)

// newDevicesCmd manages the virtual microphone, speaker and webcam.
func newDevicesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   devicesName,
		Short: "Install, remove or inspect the virtual audio and camera devices",
	}

	cmd.AddCommand(newDevicesInstallCmd(), newDevicesRemoveCmd(), newDevicesStatusCmd())

	return cmd
}

// newDevicesInstallCmd installs the bundled drivers.
func newDevicesInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   verbInstall,
		Short: "Install the bundled virtual-device drivers (privileged)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := devices.RequirePrivilege(verbInstall); err != nil {
				return err
			}

			results, err := devices.Install(cmd.Context())
			if err != nil {
				return err
			}

			return deviceTable(cmd, results)
		},
	}
}

// newDevicesRemoveCmd removes the installed drivers.
func newDevicesRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   verbRemove,
		Short: "Remove the virtual-device drivers (privileged)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := devices.RequirePrivilege(verbRemove); err != nil {
				return err
			}

			results, err := devices.Remove(cmd.Context())
			if err != nil {
				return err
			}

			return deviceTable(cmd, results)
		},
	}
}

// newDevicesStatusCmd reports each device's state.
func newDevicesStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   verbStatus,
		Short: "Show the state of the virtual devices",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			results, err := devices.Status(cmd.Context())
			if err != nil {
				return err
			}

			return deviceTable(cmd, results)
		},
	}
}

// deviceTable renders results as an aligned DEVICE/STATE/NEXT STEP table.
func deviceTable(cmd *cobra.Command, results []devices.Result) error {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	if err := row(w, "DEVICE", "STATE", "NEXT STEP"); err != nil {
		return err
	}

	for _, result := range results {
		if err := row(w, string(result.Kind), string(result.State), result.NextStep); err != nil {
			return err
		}
	}

	return w.Flush()
}
