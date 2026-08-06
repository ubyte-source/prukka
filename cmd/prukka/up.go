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

// newUpCmd starts the daemon in the foreground and opens the dashboard.
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

// openWhenReady polls /healthz then opens the dashboard, giving up silently.
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

// dashboardURL renders the two forms of the dashboard address: launch appends
// the control token as a URL fragment, and only display, which carries no
// secret, may be rendered anywhere the address outlives the moment.
func dashboardURL(base string) (launch, display string) {
	display = base + "/ui/"
	token, err := control.ReadToken(paths.TokenPath())
	if err != nil {
		return display, display
	}

	return display + "#token=" + token, display
}

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
