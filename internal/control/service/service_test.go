package service_test

import (
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/control/service"
)

func TestRunningRecognizesLiveStates(t *testing.T) {
	t.Parallel()

	for state, want := range map[string]bool{
		"running":                 true,
		"active":                  true,
		"inactive":                false,
		"not installed":           false,
		"installed (not running)": false,
		"":                        false,
	} {
		if got := service.Running(state); got != want {
			t.Errorf("Running(%q) = %v, want %v", state, got, want)
		}
	}
}

func TestRenderedContainsTheDaemonInvocation(t *testing.T) {
	t.Parallel()

	path, content, err := service.Rendered(&service.Options{
		ExecPath:   "/usr/local/bin/prukka",
		ConfigPath: "/etc/prukka/config.yaml",
	})
	if err != nil {
		t.Fatalf("Rendered returned error: %v", err)
	}

	if path == "" {
		t.Fatal("Rendered returned an empty path")
	}

	// Whatever the platform format, the definition must run the daemon with
	// the configured binary and config.
	for _, want := range []string{"/usr/local/bin/prukka", "daemon", "/etc/prukka/config.yaml"} {
		if !strings.Contains(content, want) {
			t.Errorf("rendered definition missing %q:\n%s", want, content)
		}
	}
}

func TestRenderedOmitsConfigWhenUnset(t *testing.T) {
	t.Parallel()

	_, content, err := service.Rendered(&service.Options{ExecPath: "/usr/local/bin/prukka"})
	if err != nil {
		t.Fatalf("Rendered returned error: %v", err)
	}

	if strings.Contains(content, "--config") {
		t.Fatalf("rendered definition has --config without a config path:\n%s", content)
	}
}
