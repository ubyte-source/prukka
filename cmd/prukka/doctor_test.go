package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/devices"
	"github.com/ubyte-source/prukka/internal/doctor"
)

// TestDaemonCheckDoesNotStackHints: the check interpolated the raw error
// inside its own "start it with ..." sentence, so a cause that already names
// its next step printed two hints on one very long line and the actionable
// tail was the first casualty of terminal wrapping.
func TestDaemonCheckDoesNotStackHints(t *testing.T) {
	state := t.TempDir()
	t.Setenv("PRUKKA_STATE", state)
	cfgPath := filepath.Join(state, "config.yaml")
	if err := os.WriteFile(cfgPath, nil, 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetContext(t.Context())

	check := daemonCheck(cmd, &rootFlags{config: cfgPath, logLevel: "info"})
	if check.Status != doctor.StatusWarn {
		t.Fatalf("daemonCheck without a daemon = %+v, want a warning", check)
	}
	if strings.Count(check.Detail, "prukka up") != 1 {
		t.Fatalf("the daemon check stacks hints on one line: %q", check.Detail)
	}
}

// TestDevicesCheckWarnsWhenNothingInstalled: a fresh machine gets the
// one install command, not a failure.
func TestDevicesCheckWarnsWhenNothingInstalled(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	check := devicesCheck(t.Context())
	if check.Status != doctor.StatusWarn || !strings.Contains(check.Detail, devices.InstallHint()) {
		t.Fatalf("devicesCheck = %+v, want the install hint", check)
	}
}

// TestDeviceVerdictInstallHintBeatsManualNotes: a fresh Windows machine
// reports manual audio drivers, yet the first step is still the install
// command.
func TestDeviceVerdictInstallHintBeatsManualNotes(t *testing.T) {
	t.Parallel()

	check := deviceVerdict([]devices.Result{
		{Kind: devices.Microphone, State: devices.StateManual, NextStep: "sign it"},
		{Kind: devices.Speaker, State: devices.StateManual, NextStep: "sign it"},
		{Kind: devices.Webcam, State: devices.StateMissing},
	})
	if check.Status != doctor.StatusWarn || !strings.Contains(check.Detail, devices.InstallHint()) {
		t.Fatalf("deviceVerdict = %+v, want the install hint", check)
	}
}

// TestDeviceVerdictSurfacesManualNextStep: once something is installed,
// the remaining manual device names its next step.
func TestDeviceVerdictSurfacesManualNextStep(t *testing.T) {
	t.Parallel()

	check := deviceVerdict([]devices.Result{
		{Kind: devices.Webcam, State: devices.StateInstalled},
		{Kind: devices.Microphone, State: devices.StateManual, NextStep: "sign it"},
	})
	if check.Status != doctor.StatusWarn || check.Detail != "microphone: sign it" {
		t.Fatalf("deviceVerdict = %+v, want the manual next step", check)
	}
}

// TestDeviceVerdictCountsInstalled: all devices in place reads OK with
// the tally.
func TestDeviceVerdictCountsInstalled(t *testing.T) {
	t.Parallel()

	check := deviceVerdict([]devices.Result{
		{Kind: devices.Microphone, State: devices.StateInstalled},
		{Kind: devices.Speaker, State: devices.StateInstalled},
		{Kind: devices.Webcam, State: devices.StateInstalled},
	})
	if check.Status != doctor.StatusOK || check.Detail != "3 of 3 installed" {
		t.Fatalf("deviceVerdict = %+v, want 3 of 3 installed", check)
	}
}

// TestDeviceAttentionWordsTheStates: outdated and manual states carry
// their next step, settled states stay quiet.
func TestDeviceAttentionWordsTheStates(t *testing.T) {
	t.Parallel()

	outdated := deviceAttention(devices.Result{Kind: devices.Webcam, State: devices.StateOutdated})
	if !strings.Contains(outdated, "outdated") || !strings.Contains(outdated, "webcam") {
		t.Fatalf("outdated attention = %q", outdated)
	}

	manual := deviceAttention(devices.Result{Kind: devices.Microphone, State: devices.StateManual, NextStep: "sign it"})
	if manual != "microphone: sign it" {
		t.Fatalf("manual attention = %q", manual)
	}

	if got := deviceAttention(devices.Result{Kind: devices.Speaker, State: devices.StateInstalled}); got != "" {
		t.Fatalf("installed attention = %q, want none", got)
	}
}
