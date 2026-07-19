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

	"github.com/ubyte-source/prukka/internal/hostos"
)

// The tray mark, rasterized from assets/brand/prukka.svg at 22 px with its
// transparent background intact: the full-color icon for Linux bars, and an
// alpha-only template macOS recolors to match light and dark appearance.
var (
	//go:embed icon.png
	iconPNG []byte
	//go:embed icon_template.png
	iconTemplatePNG []byte
)

// statsInterval paces the daemon polls behind the status line.
const statsInterval = 5 * time.Second

// Stats carries the live numbers the tray renders.
type Stats struct {
	Sessions int
}

// StatsSource provides the live numbers the tray renders; cmd/prukka wires
// an implementation backed by the control plane.
type StatsSource interface {
	Stats(ctx context.Context) (Stats, error)
}

// Config wires the tray. DashboardURL is what the browser is launched with and
// carries the control token in its fragment; DashboardDisplay is the same
// address without it, and is the only one of the two that may be rendered
// where the address outlives the moment — a log line keeps whatever it was
// handed for as long as the log lives.
type Config struct {
	Stats            StatsSource
	Log              *slog.Logger
	DashboardURL     string
	DashboardDisplay string
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
	// until packaging ships one. Elsewhere one call carries both marks:
	// macOS takes the template, Linux backends fall back to the color icon.
	if runtime.GOOS == hostos.Windows {
		systray.SetTitle("Prukka")
	} else {
		systray.SetTemplateIcon(iconTemplatePNG, iconPNG)
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
				cfg.Log.Warn("opening dashboard", "url", cfg.DashboardDisplay, "err", err)
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
	status.SetTitle(statusTitle(ctx, cfg))
}

// statusTitle polls the daemon once and renders the status line. The tray is
// the only consumer of the poll error, so its cause is logged here — at Debug,
// because a stopped daemon repeats every tick and is not an operator fault.
func statusTitle(ctx context.Context, cfg *Config) string {
	pollCtx, cancel := context.WithTimeout(ctx, statsInterval)
	defer cancel()

	stats, err := cfg.Stats.Stats(pollCtx)
	if err != nil {
		cfg.Log.Debug("tray stats poll", "err", err)

		return "Daemon unreachable"
	}

	return fmt.Sprintf("Live: %d sessions", stats.Sessions)
}
