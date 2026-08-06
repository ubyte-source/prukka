package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestRootCommandResultsGoToStdout(t *testing.T) {
	t.Parallel()

	if newRootCmd().OutOrStderr() != os.Stdout {
		t.Fatal("root command results must default to stdout, not cobra's stderr fallback")
	}
}

func TestRootRegistersEveryCommand(t *testing.T) {
	t.Parallel()

	root := newRootCmd()

	want := []string{
		daemonName, "up", "tray", "session", "doctor", "service",
		"devices", "stats", "setup", "update", "version",
	}

	registered := map[string]bool{}
	for _, c := range root.Commands() {
		registered[c.Name()] = true
	}

	for _, name := range want {
		if !registered[name] {
			t.Fatalf("command %q is not registered on the root", name)
		}
	}
}

func TestRootPersistentFlags(t *testing.T) {
	t.Parallel()

	root := newRootCmd()

	for _, name := range []string{"config", "log-level"} {
		if root.PersistentFlags().Lookup(name) == nil {
			t.Fatalf("persistent flag --%s is missing", name)
		}
	}

	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"--help"})

	if err := root.Execute(); err != nil {
		t.Fatalf("--help returned error: %v", err)
	}
}

func TestConfigFlagAdvertisesItsValueType(t *testing.T) {
	t.Parallel()

	usage := newRootCmd().PersistentFlags().FlagUsages()
	if !strings.Contains(usage, "--config path") {
		t.Fatalf("--config does not advertise a path placeholder:\n%s", usage)
	}
}
