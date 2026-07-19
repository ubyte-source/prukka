package observability_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/ubyte-source/prukka/internal/observability"
)

// TestNewLoggerEmitsComponentJSON: every line is machine-readable JSON
// carrying the component, and the level threshold filters below it.
func TestNewLoggerEmitsComponentJSON(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	log := observability.NewLogger(&buf, slog.LevelInfo, "ingest")
	log.Debug("filtered out")
	log.Info("hello", "key", "value")

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("output is not one JSON line: %v (%q)", err, buf.String())
	}

	if line["component"] != "ingest" || line["msg"] != "hello" || line["key"] != "value" {
		t.Fatalf("line = %v, want component/msg/key attrs", line)
	}
}

func TestParseLevel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"", slog.LevelInfo},
		{"info", slog.LevelInfo},
		{" WARN ", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
	}

	for _, tc := range cases {
		got, err := observability.ParseLevel(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("ParseLevel(%q) = (%v, %v), want %v", tc.in, got, err, tc.want)
		}
	}

	if _, err := observability.ParseLevel("loud"); err == nil {
		t.Fatal("an unknown level parsed without error")
	}
}
