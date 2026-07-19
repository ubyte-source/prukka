package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"

	"github.com/ubyte-source/prukka/internal/control"
	"github.com/ubyte-source/prukka/internal/paths"
)

// readyPollInterval paces the /healthz polls before opening the browser.
const readyPollInterval = 200 * time.Millisecond

// readyPollAttempts bounds how long `up` waits for the daemon (10 s).
const readyPollAttempts = 50

// newUpCmd starts the daemon in the foreground and opens the dashboard —
// the 60-second path from install to a live session.
func newUpCmd(flags *rootFlags) *cobra.Command {
	var noBrowser bool

	cmd := &cobra.Command{
		Use:   "up",
		Short: "Start the daemon in the foreground and open the dashboard",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			holder, log, err := flags.holder()
			if err != nil {
				return err
			}

			if !noBrowser {
				go openWhenReady(cmd.Context(), holder.Current().Daemon.HTTP, log)
			}

			return runDaemon(cmd.Context(), holder, log, "")
		},
	}

	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "do not open the dashboard in a browser")

	return cmd
}

// openWhenReady polls /healthz then opens the dashboard; it gives up
// silently — the daemon's own error is the message that matters.
func openWhenReady(ctx context.Context, addr string, log *slog.Logger) {
	base := "http://" + addr
	client := &http.Client{Timeout: time.Second}

	for range readyPollAttempts {
		select {
		case <-ctx.Done():
			return
		case <-time.After(readyPollInterval):
		}

		if !healthy(ctx, client, base+"/healthz") {
			continue
		}

		launch, display := dashboardURL(base)
		if err := browser.OpenURL(launch); err != nil {
			log.Warn("opening dashboard", "url", display, "err", err)
		}

		return
	}
}

// dashboardURL renders the two forms of the dashboard address. The launch form
// appends the control token as a URL fragment, which never leaves the browser;
// the display form carries no secret, and it is the only one that may be
// rendered where the address outlives the moment — a log line, an error, a
// terminal scrollback keep whatever they were handed for as long as they live.
// An operator the browser could not be launched for opens the display form and
// pastes the token into the dashboard's token field.
func dashboardURL(base string) (launch, display string) {
	display = base + "/ui/"
	token, err := control.ReadToken(paths.TokenPath())
	if err != nil {
		return display, display
	}

	return display + "#token=" + token, display
}

// healthy performs one health poll.
func healthy(ctx context.Context, client *http.Client, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return false
	}

	resp, doErr := client.Do(req)
	if doErr != nil {
		return false
	}

	ok := resp.StatusCode == http.StatusOK
	if closeErr := resp.Body.Close(); closeErr != nil {
		return false
	}

	return ok
}
