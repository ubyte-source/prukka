//go:build linux

package service

import (
	"strings"
	"testing"
)

// TestRenderedSystemdUnit: the unit starts the daemon with the requested
// config, restarts on failure and survives reboots.
func TestRenderedSystemdUnit(t *testing.T) {
	t.Parallel()

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
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("unit lacks %q:\n%s", want, content)
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
		t.Fatalf("unit carries --config without a path:\n%s", content)
	}
}
