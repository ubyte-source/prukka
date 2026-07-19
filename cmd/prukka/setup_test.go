package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/core/config"
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
	engineRuns := 0
	engine := func(context.Context, *config.Config, io.Writer) error {
		engineRuns++

		return nil
	}
	cmd := newSetupCommand(&rootFlags{}, install, engine)
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
	// The machine path stays ffmpeg-only: CI must not pull engine models.
	if engineRuns != 0 {
		t.Fatalf("setup --print-path ran the engine install %d times", engineRuns)
	}
}

// TestSetupInstallsEngineAfterFFmpeg: the human path chains both installers
// and reports both dependencies.
func TestSetupInstallsEngineAfterFFmpeg(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())
	t.Setenv("PRUKKA_ENGINE_BIN", "")

	install := func(_ context.Context, _ string, _ io.Writer) (string, error) {
		return "/verified/ffmpeg", nil
	}
	var engineCfg *config.Config
	engine := func(_ context.Context, cfg *config.Config, _ io.Writer) error {
		engineCfg = cfg

		return nil
	}
	cmd := newSetupCommand(&rootFlags{}, install, engine)
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if engineCfg == nil {
		t.Fatal("setup did not run the engine install")
	}
	if !strings.Contains(output.String(), "speech engine") {
		t.Fatalf("setup output does not report the engine: %q", output.String())
	}
}

// TestRequiredPackIDsFollowConfiguredCapabilities: pack selection mirrors the
// configured routes and voices, and drops voices when dubbing is off.
func TestRequiredPackIDsFollowConfiguredCapabilities(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	got := strings.Join(requiredPackIDs(cfg), ",")
	want := "stt-core,mt-it-en,mt-en-it,voice-en,voice-it"
	if got != want {
		t.Fatalf("pack ids = %q, want %q", got, want)
	}

	cfg.Providers.Voices = config.VoicesOff
	got = strings.Join(requiredPackIDs(cfg), ",")
	if strings.Contains(got, "voice-") {
		t.Fatalf("voices off must not require voice packs: %q", got)
	}
}
