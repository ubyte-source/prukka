package main

import (
	"log/slog"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core/config"
)

// TestSessionDefaultsMirrorTheConfig: what the wizard seeds new sessions
// with must come from configuration, not from constants in the server.
func TestSessionDefaultsMirrorTheConfig(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.Defaults.Subs = "burn"
	cfg.Defaults.Bed = "-9dB"
	cfg.Budgets.PerSessionEURPerHour = 1.5
	cfg.Defaults.Delay = config.Duration(4 * time.Second)

	got := sessionDefaults(cfg)

	if got.Subs != "burn" || got.Bed != "-9dB" ||
		got.BudgetEURPerHour != 1.5 || got.Delay != 4*time.Second {
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
