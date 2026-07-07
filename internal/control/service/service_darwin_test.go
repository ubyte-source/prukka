//go:build darwin

package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary impersonate launchctl when re-exec'd
// through the PATH symlink planted by fakeLaunchctl.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "launchctl" {
		os.Exit(fakeTool())
	}

	os.Exit(m.Run())
}

// fakeTool appends its arguments to the log named by PRUKKA_FAKE_LOG and
// fails on the verb named by PRUKKA_FAKE_FAIL_VERB.
func fakeTool() int {
	f, openErr := os.OpenFile(filepath.Clean(os.Getenv("PRUKKA_FAKE_LOG")), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if openErr != nil {
		return 2
	}

	_, writeErr := fmt.Fprintln(f, strings.Join(os.Args[1:], " "))
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return 2
	}

	if len(os.Args) > 1 && os.Args[1] == os.Getenv("PRUKKA_FAKE_FAIL_VERB") {
		if code, err := strconv.Atoi(os.Getenv("PRUKKA_FAKE_FAIL_CODE")); err == nil {
			return code
		}

		return 1
	}

	return 0
}

// fakeLaunchctl puts the test binary first on PATH under the launchctl
// name, logging invocations and failing on the given verb; it returns the
// log path. It also points HOME at a temp dir so plists land there and
// removes the retry pacing.
func fakeLaunchctl(t *testing.T, failingVerb string) string {
	t.Helper()

	exe, exeErr := os.Executable()
	if exeErr != nil {
		t.Fatalf("locate test binary: %v", exeErr)
	}

	bin := t.TempDir()
	if linkErr := os.Symlink(exe, filepath.Join(bin, "launchctl")); linkErr != nil {
		t.Fatalf("plant fake launchctl: %v", linkErr)
	}

	log := filepath.Join(bin, "calls.log")

	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PRUKKA_FAKE_LOG", log)
	t.Setenv("PRUKKA_FAKE_FAIL_VERB", failingVerb)

	prev := bootstrapRetryDelay
	bootstrapRetryDelay = time.Millisecond

	t.Cleanup(func() { bootstrapRetryDelay = prev })

	return log
}

// launchctlCalls reads the recorded fake invocations.
func launchctlCalls(t *testing.T, log string) string {
	t.Helper()

	out, err := os.ReadFile(filepath.Clean(log))
	if err != nil {
		t.Fatalf("read launchctl log: %v", err)
	}

	return string(out)
}

// TestRestartKickstartsTheDaemon: a loaded agent is relaunched in place.
func TestRestartKickstartsTheDaemon(t *testing.T) {
	log := fakeLaunchctl(t, "")

	if err := restart(t.Context()); err != nil {
		t.Fatalf("restart: %v", err)
	}

	if got := launchctlCalls(t, log); got != "kickstart -k "+agentTarget()+"\n" {
		t.Fatalf("launchctl calls = %q", got)
	}
}

// TestRestartBootstrapsWhenNotLoaded: an installed agent that was never
// bootstrapped is loaded instead of kicked.
func TestRestartBootstrapsWhenNotLoaded(t *testing.T) {
	log := fakeLaunchctl(t, "kickstart")

	if err := install(t.Context(), &Options{ExecPath: "/usr/local/bin/prukka"}); err != nil {
		t.Fatalf("install: %v", err)
	}

	if err := restart(t.Context()); err != nil {
		t.Fatalf("restart: %v", err)
	}

	path, pathErr := agentPlistPath()
	if pathErr != nil {
		t.Fatalf("agent plist path: %v", pathErr)
	}

	if got := launchctlCalls(t, log); !strings.HasSuffix(got, fmt.Sprintf("bootstrap gui/%d %s\n", os.Getuid(), path)) {
		t.Fatalf("launchctl calls = %q", got)
	}
}

// TestInstallWritesAgentPlist: without --now only the plist is written,
// into the user's LaunchAgents directory.
func TestInstallWritesAgentPlist(t *testing.T) {
	log := fakeLaunchctl(t, "")

	if err := install(t.Context(), &Options{ExecPath: "/usr/local/bin/prukka"}); err != nil {
		t.Fatalf("install: %v", err)
	}

	path, pathErr := agentPlistPath()
	if pathErr != nil {
		t.Fatalf("agent plist path: %v", pathErr)
	}

	if !strings.HasPrefix(path, os.Getenv("HOME")+"/Library/LaunchAgents/") {
		t.Fatalf("plist path %q is not under the user's LaunchAgents", path)
	}

	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("plist not written: %v", statErr)
	}

	assertDaemonLogCaptured(t, path)

	if _, readErr := os.ReadFile(filepath.Clean(log)); !os.IsNotExist(readErr) {
		t.Fatalf("launchctl was invoked without --now: %v", readErr)
	}
}

// assertDaemonLogCaptured: without log capture every daemon line vanishes
// under launchd; the plist must route both streams and the log directory
// must exist.
func assertDaemonLogCaptured(t *testing.T, plistPath string) {
	t.Helper()

	plist, readErr := os.ReadFile(filepath.Clean(plistPath))
	if readErr != nil {
		t.Fatalf("read plist: %v", readErr)
	}

	logPath, logErr := daemonLogPath()
	if logErr != nil {
		t.Fatalf("daemon log path: %v", logErr)
	}

	if !strings.Contains(string(plist), "<key>StandardErrorPath</key>") ||
		!strings.Contains(string(plist), "<string>"+logPath+"</string>") {
		t.Fatalf("plist does not capture the daemon log at %q:\n%s", logPath, plist)
	}

	if _, statErr := os.Stat(filepath.Dir(logPath)); statErr != nil {
		t.Fatalf("log directory not created: %v", statErr)
	}
}

// TestInstallNowBootsOutThenBootstraps: a reinstall replaces any live
// instance before loading the fresh one.
func TestInstallNowBootsOutThenBootstraps(t *testing.T) {
	log := fakeLaunchctl(t, "")

	if err := install(t.Context(), &Options{ExecPath: "/usr/local/bin/prukka", Now: true}); err != nil {
		t.Fatalf("install: %v", err)
	}

	path, pathErr := agentPlistPath()
	if pathErr != nil {
		t.Fatalf("agent plist path: %v", pathErr)
	}

	want := "bootout " + agentTarget() + "\n" + fmt.Sprintf("bootstrap gui/%d %s\n", os.Getuid(), path)
	if got := launchctlCalls(t, log); got != want {
		t.Fatalf("launchctl calls = %q, want %q", got, want)
	}
}

// TestBootstrapRetriesWhileLaunchdSettles: the transient I/O error (EIO)
// after a bootout is retried a bounded number of times before surfacing.
func TestBootstrapRetriesWhileLaunchdSettles(t *testing.T) {
	log := fakeLaunchctl(t, "bootstrap")
	t.Setenv("PRUKKA_FAKE_FAIL_CODE", "5")

	err := install(t.Context(), &Options{ExecPath: "/usr/local/bin/prukka", Now: true})
	if err == nil {
		t.Fatal("install succeeded despite bootstrap failing")
	}

	got := strings.Count(launchctlCalls(t, log), "bootstrap ")
	if got != bootstrapAttempts {
		t.Fatalf("bootstrap attempts = %d, want %d", got, bootstrapAttempts)
	}
}

// TestBootstrapSurfacesPermanentErrorsImmediately: a non-EIO failure
// (wrong domain, missing plist) must not burn the ten-second retry loop.
func TestBootstrapSurfacesPermanentErrorsImmediately(t *testing.T) {
	log := fakeLaunchctl(t, "bootstrap")
	t.Setenv("PRUKKA_FAKE_FAIL_CODE", "125")

	err := install(t.Context(), &Options{ExecPath: "/usr/local/bin/prukka", Now: true})
	if err == nil {
		t.Fatal("install succeeded despite bootstrap failing")
	}

	if got := strings.Count(launchctlCalls(t, log), "bootstrap "); got != 1 {
		t.Fatalf("bootstrap attempts = %d, want 1 (permanent failure)", got)
	}
}

// TestRemoveDeletesPlistAndBootsOut: removal unloads the agent and
// deletes its plist; a second removal is a no-op.
func TestRemoveDeletesPlistAndBootsOut(t *testing.T) {
	log := fakeLaunchctl(t, "")

	if err := install(t.Context(), &Options{ExecPath: "/usr/local/bin/prukka"}); err != nil {
		t.Fatalf("install: %v", err)
	}

	if err := remove(t.Context()); err != nil {
		t.Fatalf("remove: %v", err)
	}

	path, pathErr := agentPlistPath()
	if pathErr != nil {
		t.Fatalf("agent plist path: %v", pathErr)
	}

	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("plist still present after remove: %v", statErr)
	}

	if got := launchctlCalls(t, log); got != "bootout "+agentTarget()+"\n" {
		t.Fatalf("launchctl calls = %q", got)
	}

	if err := remove(t.Context()); err != nil {
		t.Fatalf("second remove: %v", err)
	}
}

func TestRemoveReportsAnAgentThatLaunchdCouldNotStop(t *testing.T) {
	fakeLaunchctl(t, "bootout")

	if err := install(t.Context(), &Options{ExecPath: "/usr/local/bin/prukka"}); err != nil {
		t.Fatalf("install: %v", err)
	}

	if err := remove(t.Context()); err == nil {
		t.Fatal("remove succeeded while launchd still reported the agent")
	}
}

// TestStatusReadsInstalledStates: launchd's answer wins; otherwise the
// plist on disk distinguishes installed from absent.
func TestStatusReadsInstalledStates(t *testing.T) {
	fakeLaunchctl(t, "print")

	got, err := status(t.Context())
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	if got != "not installed" {
		t.Fatalf("status = %q, want not installed", got)
	}

	if installErr := install(t.Context(), &Options{ExecPath: "/usr/local/bin/prukka"}); installErr != nil {
		t.Fatalf("install: %v", installErr)
	}

	got, err = status(t.Context())
	if err != nil {
		t.Fatalf("status after install: %v", err)
	}

	if got != "installed (not running)" {
		t.Fatalf("status = %q, want installed (not running)", got)
	}
}

// TestRenderedLaunchdPlist: a valid plist targeting the daemon with the
// requested config, surviving reboots.
func TestRenderedLaunchdPlist(t *testing.T) {
	t.Parallel()

	path, content, err := rendered(&Options{
		ExecPath:   "/usr/local/bin/prukka",
		ConfigPath: "/etc/prukka.yaml",
	})
	if err != nil {
		t.Fatalf("rendered returned error: %v", err)
	}

	if !strings.HasSuffix(path, ".plist") {
		t.Fatalf("path %q is not a plist", path)
	}

	for _, want := range []string{
		"io.prukka.daemon",
		"<string>/usr/local/bin/prukka</string>",
		"<string>daemon</string>",
		"<string>--config</string>",
		"<string>/etc/prukka.yaml</string>",
		"RunAtLoad",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("plist lacks %q:\n%s", want, content)
		}
	}
}

// TestRenderedEscapesXMLSpecials: an install path with XML specials must
// not corrupt the plist.
func TestRenderedEscapesXMLSpecials(t *testing.T) {
	t.Parallel()

	_, content, err := rendered(&Options{ExecPath: "/Users/me & you/prukka"})
	if err != nil {
		t.Fatalf("rendered returned error: %v", err)
	}

	if !strings.Contains(content, "<string>/Users/me &amp; you/prukka</string>") {
		t.Fatalf("plist does not escape the exec path:\n%s", content)
	}
}

// TestRenderedOmitsConfigWhenUnset: no --config flag sneaks in without a
// path.
func TestRenderedOmitsConfigWhenUnset(t *testing.T) {
	t.Parallel()

	_, content, err := rendered(&Options{ExecPath: "/usr/local/bin/prukka"})
	if err != nil {
		t.Fatalf("rendered returned error: %v", err)
	}

	if strings.Contains(content, "--config") {
		t.Fatalf("plist carries --config without a path:\n%s", content)
	}
}
