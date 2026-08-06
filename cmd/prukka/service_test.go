package main

import (
	"path/filepath"
	"testing"
)

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

func TestServiceOptionsAbsolutizeTheConfigPath(t *testing.T) {
	t.Parallel()

	opts, err := serviceOptions(&rootFlags{config: filepath.Join("relative", "prukka.yaml")}, false)
	if err != nil {
		t.Fatalf("serviceOptions: %v", err)
	}
	if !filepath.IsAbs(opts.ConfigPath) {
		t.Fatalf("ConfigPath = %q, want an absolute path", opts.ConfigPath)
	}

	empty, err := serviceOptions(&rootFlags{}, false)
	if err != nil || empty.ConfigPath != "" {
		t.Fatalf("unset config = %q (%v), want it left unset", empty.ConfigPath, err)
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
