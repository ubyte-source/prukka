package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/devices"
)

// TestDevicesCommandWiresSubcommands: install, remove and status hang
// off `prukka devices`.
func TestDevicesCommandWiresSubcommands(t *testing.T) {
	t.Parallel()

	cmd := newDevicesCmd()

	want := map[string]bool{"install": false, "remove": false, "status": false}
	for _, sub := range cmd.Commands() {
		want[sub.Name()] = true
	}

	for name, found := range want {
		if !found {
			t.Errorf("subcommand %q missing", name)
		}
	}
}

// TestDeviceTableAlignsResults: the table carries the header and one
// aligned line per device with its next step.
func TestDeviceTableAlignsResults(t *testing.T) {
	t.Parallel()

	out := &bytes.Buffer{}

	cmd := newDevicesCmd()
	cmd.SetOut(out)

	results := []devices.Result{
		{Kind: devices.Microphone, State: devices.StateInstalled},
		{Kind: devices.Webcam, State: devices.StateManual, NextStep: "approve the extension"},
	}
	if err := deviceTable(cmd, results); err != nil {
		t.Fatalf("deviceTable: %v", err)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("table has %d lines, want 3:\n%s", len(lines), out.String())
	}

	if !strings.HasPrefix(lines[0], "DEVICE") || !strings.Contains(lines[0], "NEXT STEP") {
		t.Fatalf("header = %q", lines[0])
	}

	if !strings.Contains(lines[2], "webcam") || !strings.Contains(lines[2], "approve the extension") {
		t.Fatalf("webcam row = %q", lines[2])
	}
}

// TestDevicesStatusRunsUnbundled: the status subcommand reports every
// device even in a build without embedded drivers.
func TestDevicesStatusRunsUnbundled(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	out := &bytes.Buffer{}

	cmd := newDevicesCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"status"})

	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("devices status: %v", err)
	}

	for _, device := range []string{"microphone", "speaker", "webcam"} {
		if !strings.Contains(out.String(), device) {
			t.Fatalf("output misses %s:\n%s", device, out.String())
		}
	}
}
