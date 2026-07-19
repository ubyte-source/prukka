// Package tray renders a minimal tray companion: status line, dashboard
// shortcut, quit.
package tray

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"runtime"
	"time"

	"fyne.io/systray"

	"github.com/pkg/browser"
)

//go:embed icon.png
var iconPNG []byte

// statsInterval paces the daemon polls behind the status line.
const statsInterval = 5 * time.Second

// Stats carries the live numbers the tray renders.
type Stats struct {
	Version  string
	Sessions int
}

// StatsSource provides the live numbers the tray renders; cmd/prukka wires
// an implementation backed by the control plane.
type StatsSource interface {
	Stats(ctx context.Context) (Stats, error)
}

// Config wires the tray.
type Config struct {
	Stats        StatsSource
	Log          *slog.Logger
	DashboardURL string
}

// Run renders the tray until quit or ctx end; it must run on the main
// goroutine (AppKit) and cannot fail.
func Run(ctx context.Context, cfg *Config) {
	systray.Run(func() { onReady(ctx, cfg) }, func() {})
}

// onReady builds the menu and starts the poll and click loops, which the
// tray owns until ctx ends or Quit is clicked.
func onReady(ctx context.Context, cfg *Config) {
	systray.SetTooltip("Prukka — every stream, every language")

	// Windows expects ICO icon bytes; it falls back to the executable icon
	// until packaging ships one.
	if runtime.GOOS == "windows" {
		systray.SetTitle("Prukka")
	} else {
		systray.SetIcon(iconPNG)
	}

	status := systray.AddMenuItem("Connecting to daemon…", "Daemon status")
	status.Disable()

	open := systray.AddMenuItem("Open dashboard", "Open the web dashboard in a browser")
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit", "Close the tray (the daemon keeps running)")

	go pollStats(ctx, cfg, status)
	go handleClicks(ctx, cfg, open, quit)
}

// handleClicks reacts to menu activity until quit or cancellation.
func handleClicks(ctx context.Context, cfg *Config, open, quit *systray.MenuItem) {
	for {
		select {
		case <-ctx.Done():
			systray.Quit()

			return
		case <-open.ClickedCh:
			if err := browser.OpenURL(cfg.DashboardURL); err != nil {
				cfg.Log.Warn("opening dashboard", "url", cfg.DashboardURL, "err", err)
			}
		case <-quit.ClickedCh:
			systray.Quit()

			return
		}
	}
}

// pollStats keeps the status line current.
func pollStats(ctx context.Context, cfg *Config, status *systray.MenuItem) {
	ticker := time.NewTicker(statsInterval)
	defer ticker.Stop()

	refreshStatus(ctx, cfg, status)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshStatus(ctx, cfg, status)
		}
	}
}

// refreshStatus renders one poll result onto the status item.
func refreshStatus(ctx context.Context, cfg *Config, status *systray.MenuItem) {
	pollCtx, cancel := context.WithTimeout(ctx, statsInterval)
	defer cancel()

	stats, err := cfg.Stats.Stats(pollCtx)
	if err != nil {
		status.SetTitle("Daemon unreachable")

		return
	}

	status.SetTitle(fmt.Sprintf("Live: %d sessions", stats.Sessions))
}
