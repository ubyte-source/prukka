//go:build darwin

package service

import (
	"strings"
	"testing"
)

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
