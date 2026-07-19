//go:build linux

package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSystemctl plants the shared fake tool under the systemctl name. It
// also points the config directory at a temp dir so units land there.
func fakeSystemctl(t *testing.T) string {
	t.Helper()

	log := plantFakeTool(t, "systemctl")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	return log
}

// systemctlCalls reads the recorded fake invocations.
func systemctlCalls(t *testing.T, log string) string {
	t.Helper()

	out, err := os.ReadFile(filepath.Clean(log))
	if err != nil {
		t.Fatalf("read systemctl log: %v", err)
	}

	return string(out)
}

// TestInstallEnablesTheUserUnit: the unit lands under the user's systemd
// directory and is enabled in the user manager.
func TestInstallEnablesTheUserUnit(t *testing.T) {
	log := fakeSystemctl(t)

	if err := install(t.Context(), &Options{ExecPath: "/usr/local/bin/prukka", Now: true}); err != nil {
		t.Fatalf("install: %v", err)
	}

	path, pathErr := unitPath()
	if pathErr != nil {
		t.Fatalf("unit path: %v", pathErr)
	}

	if !strings.HasSuffix(path, "/systemd/user/prukka.service") {
		t.Fatalf("unit path %q is not a user unit", path)
	}

	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("unit not written: %v", statErr)
	}

	want := "--user daemon-reload\n--user enable --now prukka.service\n"
	if got := systemctlCalls(t, log); got != want {
		t.Fatalf("systemctl calls = %q, want %q", got, want)
	}
}

// TestInstallWithoutNowOnlyEnables: without --now the unit waits for the
// next login.
func TestInstallWithoutNowOnlyEnables(t *testing.T) {
	log := fakeSystemctl(t)

	if err := install(t.Context(), &Options{ExecPath: "/usr/local/bin/prukka"}); err != nil {
		t.Fatalf("install: %v", err)
	}

	want := "--user daemon-reload\n--user enable prukka.service\n"
	if got := systemctlCalls(t, log); got != want {
		t.Fatalf("systemctl calls = %q, want %q", got, want)
	}
}

// TestRestartRestartsTheUnit: restart delegates to the user manager.
func TestRestartRestartsTheUnit(t *testing.T) {
	log := fakeSystemctl(t)

	if err := restart(t.Context()); err != nil {
		t.Fatalf("restart: %v", err)
	}

	if got := systemctlCalls(t, log); got != "--user restart prukka.service\n" {
		t.Fatalf("systemctl calls = %q", got)
	}
}

// TestRemoveDisablesAndDeletes: removal disables the unit, deletes it and
// reloads; repeating it is harmless.
func TestRemoveDisablesAndDeletes(t *testing.T) {
	log := fakeSystemctl(t)

	if err := install(t.Context(), &Options{ExecPath: "/usr/local/bin/prukka"}); err != nil {
		t.Fatalf("install: %v", err)
	}

	if err := remove(t.Context()); err != nil {
		t.Fatalf("remove: %v", err)
	}

	path, pathErr := unitPath()
	if pathErr != nil {
		t.Fatalf("unit path: %v", pathErr)
	}

	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("unit still present after remove: %v", statErr)
	}

	want := "--user disable --now prukka.service\n--user daemon-reload\n"
	if got := systemctlCalls(t, log); !strings.HasSuffix(got, want) {
		t.Fatalf("systemctl calls = %q", got)
	}

	if err := remove(t.Context()); err != nil {
		t.Fatalf("second remove: %v", err)
	}
}

// TestRemoveStillStopsAUnitWhoseFileVanished: loaded systemd state can
// outlive a manually deleted definition, so removal must still ask the
// manager to disable and stop it.
func TestRemoveStillStopsAUnitWhoseFileVanished(t *testing.T) {
	log := fakeSystemctl(t)

	if err := remove(t.Context()); err != nil {
		t.Fatalf("remove missing definition: %v", err)
	}

	want := "--user disable --now prukka.service\n--user daemon-reload\n"
	if got := systemctlCalls(t, log); got != want {
		t.Fatalf("systemctl calls = %q, want %q", got, want)
	}
}

// TestStatusReportsNotInstalled: no unit on disk short-circuits before any
// systemd query.
func TestStatusReportsNotInstalled(t *testing.T) {
	fakeSystemctl(t)

	got, err := status(t.Context())
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	if got != "not installed" {
		t.Fatalf("status = %q, want not installed", got)
	}
}

// TestRenderedSystemdUnit: the user unit starts the daemon with the
// requested config, restarts on failure and starts at login.
func TestRenderedSystemdUnit(t *testing.T) {
	path, content, err := rendered(&Options{
		ExecPath:   "/usr/local/bin/prukka",
		ConfigPath: "/etc/prukka.yaml",
	})
	if err != nil {
		t.Fatalf("rendered returned error: %v", err)
	}

	if !strings.HasSuffix(path, ".service") {
		t.Fatalf("path %q is not a systemd unit", path)
	}

	for _, want := range []string{
		"ExecStart=/usr/local/bin/prukka daemon --config /etc/prukka.yaml",
		"Restart=on-failure",
		"WantedBy=default.target",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("unit lacks %q:\n%s", want, content)
		}
	}
}

// TestRenderedQuotesSpacedPaths: an install path with spaces or a literal
// percent must survive systemd's ExecStart parsing (quoting, %% escape).
func TestRenderedQuotesSpacedPaths(t *testing.T) {
	_, content, err := rendered(&Options{
		ExecPath:   "/home/user/My Tools/prukka",
		ConfigPath: "/home/user/50%/prukka.yaml",
	})
	if err != nil {
		t.Fatalf("rendered returned error: %v", err)
	}

	want := `ExecStart="/home/user/My Tools/prukka" daemon --config /home/user/50%%/prukka.yaml`
	if !strings.Contains(content, want) {
		t.Fatalf("unit lacks %q:\n%s", want, content)
	}
}
