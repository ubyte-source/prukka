package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/observability"
)

func TestRunDaemonReturnsPprofBindFailure(t *testing.T) {
	// Keep the Unix-domain control socket below Darwin's short sockaddr_un
	// limit; t.TempDir includes the full test name and can exceed it under
	// repeated runs before the pprof assertion is reached.
	state, err := os.MkdirTemp("", "prukka-daemon-")
	if err != nil {
		t.Fatalf("create short state dir: %v", err)
	}
	t.Cleanup(func() {
		if removeErr := os.RemoveAll(state); removeErr != nil {
			t.Errorf("remove short state dir: %v", removeErr)
		}
	})
	t.Setenv("PRUKKA_STATE", state)

	configPath := filepath.Join(state, "config.yaml")
	if writeErr := os.WriteFile(configPath, []byte("daemon:\n  http: 127.0.0.1:0\n"), 0o600); writeErr != nil {
		t.Fatalf("write config: %v", writeErr)
	}
	holder, err := config.NewHolder(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy pprof address: %v", err)
	}
	defer func() {
		if closeErr := listener.Close(); closeErr != nil {
			t.Errorf("release pprof address: %v", closeErr)
		}
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	err = runDaemon(ctx, holder, slog.New(slog.DiscardHandler), listener.Addr().String())
	if err == nil || !strings.Contains(err.Error(), "bind pprof server") {
		t.Fatalf("runDaemon with occupied pprof address = %v, want bind failure", err)
	}
}

// TestTeeLoggerDuplicatesIntoTheFile: with --log-file the daemon's log
// lands in the file too (a Windows scheduled task captures no output), and
// the log directory is created on demand.
func TestTeeLoggerDuplicatesIntoTheFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "logs", "daemon.log")

	log, closeLog, err := teeLogger(&rootFlags{logLevel: "info"}, path)
	if err != nil {
		t.Fatalf("teeLogger returned error: %v", err)
	}

	log.Info("hello from the tee")

	if closeErr := closeLog(); closeErr != nil {
		t.Fatalf("close returned error: %v", closeErr)
	}

	out, readErr := os.ReadFile(filepath.Clean(path))
	if readErr != nil {
		t.Fatalf("read log file: %v", readErr)
	}

	if !strings.Contains(string(out), "hello from the tee") {
		t.Fatalf("log file lacks the logged line: %q", out)
	}
}

// TestSessionDefaultsMirrorTheConfig: what the wizard seeds new sessions
// with must come from configuration, not from constants in the server.
func TestSessionDefaultsMirrorTheConfig(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.Defaults.Subs = "burn"
	cfg.Defaults.Bed = "-9dB"
	cfg.Defaults.Langs = []core.Lang{"de", "fr"}
	cfg.Defaults.Delay = config.Duration(4 * time.Second)

	got := sessionDefaults(cfg)

	if got.Subs != "burn" || got.Bed != "-9dB" || len(got.Langs) != 2 || got.Langs[0] != "de" ||
		got.Delay != 4*time.Second {
		t.Fatalf("sessionDefaults = %+v, want the configured values", got)
	}
}

// TestMuxSupervisorDegradesWithoutFFmpeg: no ffmpeg anywhere must yield a
// nil supervisor — streaming reports unavailable instead of crashing.
func TestMuxSupervisorDegradesWithoutFFmpeg(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("PRUKKA_STATE", t.TempDir())

	if sup := muxSupervisor(slog.New(slog.DiscardHandler)); sup != nil {
		t.Fatal("a supervisor materialized without any ffmpeg")
	}
}

func TestResetMediaRootRemovesCrashArtifacts(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "media")
	stale := filepath.Join(root, "old-session", "subs", "en", "seg00001.vtt")
	if err := os.MkdirAll(filepath.Dir(stale), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(stale, []byte("old transcript"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := resetMediaRoot(root); err != nil {
		t.Fatalf("resetMediaRoot returned error: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("media root survived reset: %v", err)
	}
}

func TestPublishSessionCountsUsesRegistryAndRuntimeState(t *testing.T) {
	t.Parallel()

	store := session.NewStore()
	if err := store.Create(&session.Session{
		Slug:    "metrics-test",
		Profile: session.ProfileBroadcast,
		Source:  core.SourceSpec{URL: "file:///tmp/input.wav"},
		Langs:   []core.Lang{"en"},
		Flags:   map[string]string{},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	metrics := observability.NewMetrics()
	publishSessionCounts(store, metrics)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", http.NoBody)
	metrics.Handler().ServeHTTP(recorder, request)
	out := recorder.Body.String()

	for _, want := range []string{
		"prukka_sessions_registered 1",
		`prukka_sessions_by_state{state="starting"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("session metric missing %q:\n%s", want, out)
		}
	}
}

func TestReloadConfigReconfiguresOnlyForLiveLaneChanges(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("defaults:\n  delay: 1s\n"), 0o600); err != nil {
		t.Fatalf("write initial config: %v", err)
	}
	holder, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("NewHolder returned error: %v", err)
	}

	calls := 0
	reconfigure := newLaneReconfigurer(holder, func() { calls++ }).onChange
	log := slog.New(slog.DiscardHandler)
	if err := os.WriteFile(path, []byte("defaults:\n  langs: [ch]\n"), 0o600); err != nil {
		t.Fatalf("write invalid config: %v", err)
	}
	reloadConfig(holder, reconfigure, log)
	if calls != 0 {
		t.Fatalf("reconfigure calls after failed reload = %d, want 0", calls)
	}

	if err := os.WriteFile(path, []byte("defaults:\n  delay: 2s\n"), 0o600); err != nil {
		t.Fatalf("write valid config: %v", err)
	}
	reloadConfig(holder, reconfigure, log)
	if calls != 0 {
		t.Fatalf("reconfigure calls after defaults-only reload = %d, want 0", calls)
	}
	if got := holder.Current().Defaults.Delay.Std(); got != 2*time.Second {
		t.Fatalf("live delay = %v, want 2s", got)
	}

	laneConfig := []byte("providers:\n  local:\n    stt:\n      model: models/stt/large.bin\n")
	if err := os.WriteFile(path, laneConfig, 0o600); err != nil {
		t.Fatalf("write lane config: %v", err)
	}
	reloadConfig(holder, reconfigure, log)
	if calls != 1 {
		t.Fatalf("reconfigure calls after model reload = %d, want 1", calls)
	}
}

// TestLaneReconfigurerAbsorbsAdditiveInstall proves an additive engine pack
// install (syncBaseline) never restarts a live lane, and that the absorbed
// delta cannot later masquerade as a lane-relevant edit on a settings save.
func TestLaneReconfigurerAbsorbsAdditiveInstall(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("defaults:\n  delay: 1s\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	holder, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("NewHolder returned error: %v", err)
	}

	calls := 0
	lane := newLaneReconfigurer(holder, func() { calls++ })

	// A pack install grows capability additively; the daemon routes it to
	// syncBaseline, which must not reconfigure.
	if _, err := holder.Update(func(c *config.Config) {
		c.Providers.Local.MT.AddPair(core.Lang("it"), core.Lang("en"))
	}); err != nil {
		t.Fatalf("additive install update: %v", err)
	}
	lane.syncBaseline()
	if calls != 0 {
		t.Fatalf("reconfigure calls after additive install = %d, want 0", calls)
	}

	// A settings save that touches nothing lane-relevant must stay quiet: the
	// install delta was already folded into the baseline.
	lane.onChange()
	if calls != 0 {
		t.Fatalf("reconfigure calls after no-op settings save = %d, want 0", calls)
	}

	// A genuine lane-relevant change (a removed/edited capability) reconfigures.
	if _, err := holder.Update(func(c *config.Config) {
		c.Providers.Local.STT.Model = "models/stt/large.bin"
	}); err != nil {
		t.Fatalf("lane-relevant update: %v", err)
	}
	lane.onChange()
	if calls != 1 {
		t.Fatalf("reconfigure calls after real change = %d, want 1", calls)
	}
}

// TestDeviceOutputStampWatchesOnlyLabeledDeviceTargets: the encoder watchdog
// must never fingerprint non-device or unlabeled targets.
func TestDeviceOutputStampWatchesOnlyLabeledDeviceTargets(t *testing.T) {
	t.Parallel()

	if _, ok := deviceOutputStamp("rtmp://example/live"); ok {
		t.Fatal("network target must not be watchable")
	}
	if _, ok := deviceOutputStamp("device://audio/3"); ok {
		t.Fatal("unlabeled device target must not be watchable")
	}
	if _, ok := deviceOutputStamp("://bad"); ok {
		t.Fatal("unparsable target must not be watchable")
	}
	// A labeled target defers to the platform fingerprint; on hosts where
	// the label does not resolve uniquely this reports unwatchable rather
	// than guessing.
	_, _ = deviceOutputStamp("device://audio/3?label=No+Such+Device")
}
