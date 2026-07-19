package main

import (
	"bytes"
	"testing"
)

// TestRootRegistersEveryCommand: the CLI surface is the product's front
// door — a subcommand silently dropped from the root is a regression.
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

// TestRootPersistentFlags: --config and --log-level must reach every
// subcommand.
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
