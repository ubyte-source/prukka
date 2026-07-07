package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"time"
)

// pprofReadHeaderTimeout bounds the profiling server's header read so it is
// not a slow-loris target (gosec G112), even though it is loopback-only.
const pprofReadHeaderTimeout = 5 * time.Second

// servePprof serves pprof on addr until ctx ends (for PGO captures):
// opt-in, loopback-only, private mux.
func servePprof(ctx context.Context, addr string, log *slog.Logger) error {
	if addr == "" {
		return nil
	}

	if !isLoopback(addr) {
		return fmt.Errorf("pprof address %q must be loopback (e.g. 127.0.0.1:6060); profiling is local-only", addr)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: pprofReadHeaderTimeout}

	go func() {
		<-ctx.Done()

		// Derive from ctx but drop its (already-fired) cancellation so the
		// drain has its own deadline instead of aborting immediately.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Debug("pprof server shutdown", "err", err)
		}
	}()

	log.Info("pprof profiling server started (loopback only)", "addr", addr)

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("pprof server: %w", err)
	}

	return nil
}

// isLoopback reports whether host:port binds only to the local host.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}

	if host == "localhost" {
		return true
	}

	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}
