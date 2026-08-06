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

// servePprof serves pprof on addr until ctx ends; opt-in and loopback-only.
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

	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("bind pprof server: %w", err)
	}

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: pprofReadHeaderTimeout}

	go func() {
		<-ctx.Done()

		// ctx has already fired, so the drain needs its cancellation dropped.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Debug("pprof server shutdown", "err", err)
		}
	}()

	log.Info("pprof profiling server started (loopback only)", "addr", listener.Addr().String())

	if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("pprof server: %w", err)
	}

	return nil
}

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
