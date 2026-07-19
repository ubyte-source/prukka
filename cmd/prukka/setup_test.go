package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/speech"
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

// TestSetupBoundsTheFFmpegInstallByOperationDeadline: the daemon runs every
// engine operation under speech.OperationTimeout; the CLI chain must give
// its ffmpeg download the same shared bound, so a hung mirror fails that
// step by deadline instead of hanging `prukka setup` forever.
func TestSetupBoundsTheFFmpegInstallByOperationDeadline(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())
	t.Setenv("PRUKKA_ENGINE_BIN", "")

	var deadline time.Time
	var bounded bool
	install := func(ctx context.Context, _ string, _ io.Writer) (string, error) {
		deadline, bounded = ctx.Deadline()

		return "/verified/ffmpeg", nil
	}
	engine := func(context.Context, *config.Config, io.Writer) error { return nil }
	cmd := newSetupCommand(&rootFlags{}, install, engine)
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if !bounded {
		t.Fatal("the ffmpeg install ran with no deadline; the daemon bounds every operation")
	}
	remaining := time.Until(deadline)
	if remaining > speech.OperationTimeout || remaining < speech.OperationTimeout-time.Minute {
		t.Fatalf("ffmpeg deadline %v away, want the shared speech.OperationTimeout (%v)",
			remaining, speech.OperationTimeout)
	}
}

// TestInstallEngineFailsAHungCatalogByDeadline: a catalog server that accepts
// and never responds is the canonical wedged mirror. The daemon bounds this
// very fetch with speech.CatalogTimeout; the CLI chain must fail it by
// deadline, naming the operation, instead of hanging `prukka setup`.
func TestInstallEngineFailsAHungCatalogByDeadline(t *testing.T) {
	t.Setenv("PRUKKA_STATE", t.TempDir())

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	hold := make(chan struct{})
	t.Cleanup(func() {
		close(hold)
		if err := listener.Close(); err != nil {
			t.Logf("close listener: %v", err)
		}
	})
	go holdConnections(listener, hold)
	t.Setenv(speech.CatalogURLEnv, "http://"+listener.Addr().String()+"/catalog.json")

	// The production chain rides the shared speech constants (installEngine
	// passes them); minutes-long bounds cannot be waited on here, so the test
	// injects short ones through the same seam.
	done := make(chan error, 1)
	go func() {
		done <- installEngineWithin(t.Context(), config.Default(), io.Discard, 150*time.Millisecond, time.Second)
	}()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "fetch engine catalog") {
			t.Fatalf("hung catalog error = %v, want a failure naming the fetch", err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("hung catalog error = %v, want context.DeadlineExceeded in its chain", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("`prukka setup` hung on a catalog server that never responds; want a bounded deadline failure")
	}
}

// holdConnections accepts and never answers: the shape of a wedged mirror.
func holdConnections(listener net.Listener, hold <-chan struct{}) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			<-hold
			if err := conn.Close(); err != nil {
				return
			}
		}()
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

// TestRequiredPackIDsUseTheVoiceBaseLanguage: the catalog names voice packs
// by base language only, so a regional voice tag must not project onto a
// pack that can never exist (it would abort `prukka setup`).
func TestRequiredPackIDsUseTheVoiceBaseLanguage(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.Providers.Local.TTS.Voices[0].Language = "de-CH"

	got := strings.Join(requiredPackIDs(cfg), ",")
	if strings.Contains(got, "voice-de-ch") || strings.Contains(got, "voice-de-CH") {
		t.Fatalf("regional tag leaked into a pack id: %q", got)
	}
	if !strings.Contains(got, "voice-de") {
		t.Fatalf("regional voice must map to its base pack: %q", got)
	}
}
