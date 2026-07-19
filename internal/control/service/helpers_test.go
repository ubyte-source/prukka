//go:build !windows

package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestMain lets the test binary impersonate the platform's service tool when
// re-exec'd through the PATH symlink a planter installed.
func TestMain(m *testing.M) {
	switch filepath.Base(os.Args[0]) {
	case "launchctl", "systemctl":
		os.Exit(fakeTool())
	default:
		os.Exit(m.Run())
	}
}

// fakeTool appends its arguments to the log named by PRUKKA_FAKE_LOG and
// fails on the verb named by PRUKKA_FAKE_FAIL_VERB (unset: never fails).
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

// plantFakeTool puts the test binary first on PATH under the tool's name and
// wires the call log; the platform planters add their own environment.
func plantFakeTool(t *testing.T, name string) string {
	t.Helper()

	exe, exeErr := os.Executable()
	if exeErr != nil {
		t.Fatalf("locate test binary: %v", exeErr)
	}

	bin := t.TempDir()
	if linkErr := os.Symlink(exe, filepath.Join(bin, name)); linkErr != nil {
		t.Fatalf("plant fake %s: %v", name, linkErr)
	}

	log := filepath.Join(bin, "calls.log")

	t.Setenv("PATH", bin)
	t.Setenv("PRUKKA_FAKE_LOG", log)

	return log
}
