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

		url := dashboardURL(base)
		if err := browser.OpenURL(url); err != nil {
			log.Warn("opening dashboard", "url", url, "err", err)
		}

		return
	}
}

// dashboardURL appends the control token as a URL fragment, which never
// leaves the browser.
func dashboardURL(base string) string {
	token, err := control.ReadToken(paths.TokenPath())
	if err != nil {
		return base + "/ui/"
	}

	return base + "/ui/#token=" + token
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
