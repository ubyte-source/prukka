// Package observability wires the daemon's structured logging, Prometheus
// metrics and provider-fallback state.
package observability

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// NewLogger returns a JSON slog logger at the given level, tagged with the
// emitting component. Session and track IDs are attached at call sites.
func NewLogger(w io.Writer, level slog.Level, component string) *slog.Logger {
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})

	return slog.New(handler).With("component", component)
}

// ParseLevel maps a config or CLI level name onto a slog level. The empty
// string selects info.
func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q (expected debug, info, warn or error)", name)
	}
}
