package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/control/service"
	"github.com/ubyte-source/prukka/internal/devices"
)

// TestMain lets the test binary impersonate the service managers when
// re-exec'd through the PATH symlinks planted by fakeServiceManagers.
func TestMain(m *testing.M) {
	switch filepath.Base(os.Args[0]) {
	case "launchctl", "systemctl":
		os.Exit(fakeServiceManager())
	default:
		os.Exit(m.Run())
	}
}

// fakeServiceManager answers status probes from PRUKKA_FAKE_STATE and
// restart verbs from PRUKKA_FAKE_RESTART.
func fakeServiceManager() int {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--user" {
		args = args[1:]
	}

	verb := ""
	if len(args) > 0 {
		verb = args[0]
	}

	running := os.Getenv("PRUKKA_FAKE_STATE") == "running"

	switch verb {
	case "is-active":
		state, code := "inactive", 3
		if running {
			state, code = "active", 0
		}

		if _, err := os.Stdout.WriteString(state + "\n"); err != nil {
			return 2
		}

		return code
	case "print":
		if running {
			return 0
		}

		return 1
	}

	if os.Getenv("PRUKKA_FAKE_RESTART") == "fail" {
		return 1
	}

	return 0
}

// fakeServiceManagers puts fake launchctl and systemctl first on PATH so
// restartedNote sees the requested daemon state; restartOK decides whether
// the relaunch verbs succeed.
func fakeServiceManagers(t *testing.T, running, restartOK bool) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("schtasks cannot be faked through PATH symlinks")
	}

	exe, exeErr := os.Executable()
	if exeErr != nil {
		t.Fatalf("locate test binary: %v", exeErr)
	}

	bin := t.TempDir()
	for _, name := range []string{"launchctl", "systemctl"} {
		if linkErr := os.Symlink(exe, filepath.Join(bin, name)); linkErr != nil {
			t.Fatalf("plant fake %s: %v", name, linkErr)
		}
	}

	t.Setenv("PATH", bin)
	// The darwin status probe falls back to the agent plist under HOME, so
	// point it at an empty home.
	t.Setenv("HOME", t.TempDir())

	state := "stopped"
	if running {
		state = "running"
	}

	t.Setenv("PRUKKA_FAKE_STATE", state)

	restart := "ok"
	if !restartOK {
		restart = "fail"
	}

	t.Setenv("PRUKKA_FAKE_RESTART", restart)
}

// TestRestartedNoteRelaunchesTheDaemon: a running daemon is restarted and
// the confirmation says so.
func TestRestartedNoteRelaunchesTheDaemon(t *testing.T) {
	fakeServiceManagers(t, true, true)

	if got := restartedNote(t.Context()); got != " (daemon restarted)" {
		t.Fatalf("restartedNote = %q", got)
	}
}

// TestRestartedNoteFlagsAStaleDaemon: a failed restart keeps the update
// and points at the OS's restart command.
func TestRestartedNoteFlagsAStaleDaemon(t *testing.T) {
	fakeServiceManagers(t, true, false)

	got := restartedNote(t.Context())
	if !strings.Contains(got, service.RestartHint()) {
		t.Fatalf("restartedNote = %q, want the restart hint", got)
	}
}

// TestRestartedNoteSkipsAStoppedDaemon: nothing runs the old version, so
// nothing is restarted or reported.
func TestRestartedNoteSkipsAStoppedDaemon(t *testing.T) {
	fakeServiceManagers(t, false, true)

	if got := restartedNote(t.Context()); got != "" {
		t.Fatalf("restartedNote = %q, want empty", got)
	}
}

// TestDevicesNoteStaysQuietWithoutDrivers: no install record means the
// update owes the devices nothing.
func TestDevicesNoteStaysQuietWithoutDrivers(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	if got := devicesNote(t.Context()); got != "" {
		t.Fatalf("devicesNote = %q, want empty", got)
	}
}

// TestDevicesNoteFlagsAFailedRefresh: recorded drivers that cannot be
// refreshed here leave the single privileged next step.
func TestDevicesNoteFlagsAFailedRefresh(t *testing.T) {
	state := t.TempDir()
	t.Setenv("PRUKKA_STATE", state)

	markers := filepath.Join(state, "devices")
	if err := os.MkdirAll(markers, 0o700); err != nil {
		t.Fatalf("create marker dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(markers, "microphone.installed"), []byte("sum\n"), 0o600); err != nil {
		t.Fatalf("plant marker: %v", err)
	}

	got := devicesNote(t.Context())
	if !strings.Contains(got, devices.InstallHint()) {
		t.Fatalf("devicesNote = %q, want the install hint", got)
	}
}

// TestUpdateCommandShape: update is wired, explicit and argument-free.
func TestUpdateCommandShape(t *testing.T) {
	t.Parallel()

	cmd := newUpdateCmd()

	if cmd.Use != "update" || cmd.RunE == nil {
		t.Fatalf("update command miswired: Use=%q, RunE nil", cmd.Use)
	}

	if !strings.Contains(strings.ToLower(cmd.Short), "update") {
		t.Fatalf("update Short %q does not describe itself", cmd.Short)
	}

	if err := cmd.Args(cmd, []string{"extra"}); err == nil {
		t.Fatal("update accepted positional arguments")
	}
}
