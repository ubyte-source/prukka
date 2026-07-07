package main

import (
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/devices"
	"github.com/ubyte-source/prukka/internal/doctor"
)

func TestCountFailed(t *testing.T) {
	t.Parallel()

	checks := []doctor.Check{
		{Name: "a", Status: doctor.StatusOK},
		{Name: "b", Status: doctor.StatusFail},
		{Name: "c", Status: doctor.StatusWarn},
		{Name: "d", Status: doctor.StatusFail},
	}

	if got := countFailed(checks); got != 2 {
		t.Fatalf("countFailed = %d, want 2", got)
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
