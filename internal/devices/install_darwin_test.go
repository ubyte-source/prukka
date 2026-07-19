//go:build darwin

package devices

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain lets the test binary impersonate system tools when re-exec'd
// through the PATH symlinks planted by fakeTools.
func TestMain(m *testing.M) {
	switch filepath.Base(os.Args[0]) {
	case "launchctl", "killall":
		os.Exit(fakeTool())
	default:
		os.Exit(m.Run())
	}
}

// fakeTool appends its name and arguments to the log named by
// PRUKKA_FAKE_LOG and fails when its name is listed in PRUKKA_FAKE_FAIL.
func fakeTool() int {
	name := filepath.Base(os.Args[0])

	f, openErr := os.OpenFile(filepath.Clean(os.Getenv("PRUKKA_FAKE_LOG")), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if openErr != nil {
		return 2
	}

	_, writeErr := fmt.Fprintln(f, name+" "+strings.Join(os.Args[1:], " "))
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return 2
	}

	if strings.Contains(os.Getenv("PRUKKA_FAKE_FAIL"), name) {
		return 1
	}

	return 0
}

// fakeTools puts the test binary first on PATH under the launchctl and
// killall names, failing for the tools listed; it returns the call log.
func fakeTools(t *testing.T, failing string) string {
	t.Helper()

	exe, exeErr := os.Executable()
	if exeErr != nil {
		t.Fatalf("locate test binary: %v", exeErr)
	}

	bin := t.TempDir()
	for _, name := range []string{"launchctl", "killall"} {
		if linkErr := os.Symlink(exe, filepath.Join(bin, name)); linkErr != nil {
			t.Fatalf("plant fake %s: %v", name, linkErr)
		}
	}

	log := filepath.Join(bin, "calls.log")

	t.Setenv("PATH", bin)
	t.Setenv("PRUKKA_FAKE_LOG", log)
	t.Setenv("PRUKKA_FAKE_FAIL", failing)

	return log
}

// toolCalls reads the recorded fake invocations.
func toolCalls(t *testing.T, log string) string {
	t.Helper()

	out, err := os.ReadFile(filepath.Clean(log))
	if err != nil {
		t.Fatalf("read tool log: %v", err)
	}

	return string(out)
}

// TestRestartCoreaudioPrefersKickstart: when launchd cooperates, the
// daemon is kicked in place and nothing gets killed.
func TestRestartCoreaudioPrefersKickstart(t *testing.T) {
	log := fakeTools(t, "")

	if err := restartCoreaudio(t.Context()); err != nil {
		t.Fatalf("restartCoreaudio: %v", err)
	}

	if got := toolCalls(t, log); got != "launchctl kickstart -kp system/com.apple.audio.coreaudiod\n" {
		t.Fatalf("tool calls = %q", got)
	}
}

// TestRestartCoreaudioFallsBackToKill: SIP denies the kickstart, so the
// daemon is killed and launchd relaunches it.
func TestRestartCoreaudioFallsBackToKill(t *testing.T) {
	log := fakeTools(t, "launchctl")

	if err := restartCoreaudio(t.Context()); err != nil {
		t.Fatalf("restartCoreaudio: %v", err)
	}

	if got := toolCalls(t, log); !strings.HasSuffix(got, "killall coreaudiod\n") {
		t.Fatalf("tool calls = %q", got)
	}
}

// TestRestartCoreaudioSurfacesBothFailures: when neither route works, the
// error names both attempts.
func TestRestartCoreaudioSurfacesBothFailures(t *testing.T) {
	fakeTools(t, "launchctl,killall")

	err := restartCoreaudio(t.Context())
	if err == nil {
		t.Fatal("restartCoreaudio succeeded despite both tools failing")
	}

	for _, want := range []string{"kickstart", "killall"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err, want)
		}
	}
}

// TestStatusReadsTheMarkers: each device reports its own record, in
// stable order.
func TestStatusReadsTheMarkers(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	if err := writeMarker(Microphone, payloadSum([]byte("hal"))); err != nil {
		t.Fatalf("writeMarker: %v", err)
	}

	results, err := status(t.Context())
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	want := map[Kind]State{Microphone: StateInstalled, Speaker: StateMissing, Webcam: StateMissing}
	if len(results) != len(want) {
		t.Fatalf("status returned %d results, want %d", len(results), len(want))
	}

	for _, result := range results {
		if result.State != want[result.Kind] {
			t.Errorf("%s = %q, want %q", result.Kind, result.State, want[result.Kind])
		}
	}
}

// TestAudioBundlesNameTheHALPlugins: the install targets are the bundle
// names the build scripts produce.
func TestAudioBundlesNameTheHALPlugins(t *testing.T) {
	t.Parallel()

	if audioBundles[Microphone] != "PrukkaMic.driver" || audioBundles[Speaker] != "PrukkaSpeaker.driver" {
		t.Fatalf("audioBundles = %v", audioBundles)
	}
}
