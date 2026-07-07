package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

// TestSetupCommandShape: setup is wired, documented and argument-free.
func TestSetupCommandShape(t *testing.T) {
	t.Parallel()

	cmd := newSetupCmd(&rootFlags{})

	if cmd.Use != "setup" || cmd.RunE == nil {
		t.Fatalf("setup command miswired: Use=%q, RunE nil", cmd.Use)
	}

	if !strings.Contains(strings.ToLower(cmd.Short), "ffmpeg") {
		t.Fatalf("setup Short %q does not say what it installs", cmd.Short)
	}

	if err := cmd.Args(cmd, []string{"extra"}); err == nil {
		t.Fatal("setup accepted positional arguments")
	}
	if cmd.Flags().Lookup("print-path") == nil {
		t.Fatal("setup has no --print-path flag")
	}
}

func TestSetupPrintPathIsMachineReadable(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	var progress io.Writer
	install := func(_ context.Context, _ string, writer io.Writer) (string, error) {
		progress = writer

		return "/verified/ffmpeg", nil
	}
	cmd := newSetupCommand(install)
	var output, errOutput bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&errOutput)
	cmd.SetArgs([]string{"--print-path"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup --print-path: %v", err)
	}
	if output.String() != "/verified/ffmpeg\n" {
		t.Fatalf("setup --print-path output = %q", output.String())
	}
	// ffmpeg_path=$(prukka setup --print-path) in CI: the path must reach
	// stdout and nothing may land on stderr.
	if errOutput.Len() != 0 {
		t.Fatalf("setup --print-path wrote to stderr: %q", errOutput.String())
	}
	if progress != io.Discard {
		t.Fatalf("setup --print-path progress writer = %T, want io.Discard", progress)
	}
}
