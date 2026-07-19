package main

import (
	"testing"
)

// TestServiceCommandWiresSubcommands: install, remove, restart and status
// are all reachable.
func TestServiceCommandWiresSubcommands(t *testing.T) {
	t.Parallel()

	cmd := newServiceCmd(&rootFlags{})

	for _, want := range []string{"install", "remove", "restart", "status"} {
		found := false
		for _, sub := range cmd.Commands() {
			if sub.Name() == want {
				found = true

				break
			}
		}

		if !found {
			t.Errorf("service command lacks %q subcommand", want)
		}
	}
}

func TestStartedSuffix(t *testing.T) {
	t.Parallel()

	if startedSuffix(true) != " and started" {
		t.Fatal("startedSuffix(true) wrong")
	}

	if startedSuffix(false) == "" {
		t.Fatal("startedSuffix(false) should explain the login behavior")
	}
}
