// Package control implements the control plane: token-authenticated gRPC
// over a local socket/pipe, its REST mirror, SSE and the dashboard.
package control

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"

	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/core/session"

	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

// httpHeaderTimeout bounds header reads so slow clients cannot pin the
// dashboard listener.
const httpHeaderTimeout = 10 * time.Second

// httpReadTimeout also bounds the small REST request bodies. The HTTP surface
// has no upload endpoint; long-lived SSE and media responses are write-side.
const httpReadTimeout = 15 * time.Second

// httpIdleTimeout releases inactive keep-alive connections without imposing a
// write deadline on SSE or media streams.
const httpIdleTimeout = 60 * time.Second

// httpMaxHeaderBytes is ample for the bearer token and browser headers while
// keeping one connection from reserving the net/http default megabyte.
const httpMaxHeaderBytes = 64 << 10

// shutdownTimeout bounds the graceful HTTP drain on shutdown.
const shutdownTimeout = 5 * time.Second

// DataPlane bundles the read ports the HTTP surface serves media from —
// caption documents, live audio streams, the HLS tree — and the logger
// handlers report post-response I/O failures to.
type DataPlane struct {
	Docs    CaptionDocs
	Streams AudioStreams
	Media   MediaTree
	Log     *slog.Logger
}

// endpoints holds where the daemon listens and how its surfaces are
// guarded, all resolved from configuration at construction.
type endpoints struct {
	httpAddr   string
	ipcPath    string
	tokenPath  string
	corsOrigin string
	ipcTLS     bool
}

// Server runs the control plane and HTTP surface for one daemon.
type Server struct {
	log     *slog.Logger
	store   *session.Store
	service *Service
	data    DataPlane
	metrics http.Handler
	on      endpoints
}

// NewServer wires a control-plane server; call Run to serve. All wiring
// inputs come from cmd/prukka. metrics may be nil to omit /metrics.
func NewServer(
	cfg *config.Config, store *session.Store, svc *Service,
	data DataPlane, metrics http.Handler, log *slog.Logger,
) *Server {
	return &Server{
		log:     log,
		store:   store,
		service: svc,
		data:    data,
		metrics: metrics,
		on: endpoints{
			httpAddr:   cfg.Daemon.HTTP,
			ipcPath:    config.IPCPath(),
			tokenPath:  config.TokenPath(),
			corsOrigin: cfg.Daemon.CORSOrigin,
			ipcTLS:     cfg.Control.IPCTLS,
		},
	}
}

// Run serves until ctx ends. It owns every listener and goroutine it starts
// and returns nil on a clean, canceled shutdown.
func (s *Server) Run(ctx context.Context) error {
	return s.RunAfterBind(ctx, nil)
}

// RunAfterBind reserves both control endpoints, then runs afterBind before
// either endpoint can serve. A failing hook closes both listeners.
func (s *Server) RunAfterBind(ctx context.Context, afterBind func() error) error {
	token, err := LoadOrCreateToken(s.on.tokenPath)
	if err != nil {
		return err
	}

	ipcListener, ipcErr := listenIPC(ctx, s.on.ipcPath)
	if ipcErr != nil {
		return ipcErr
	}

	ipcListener, gatewayTLS, tlsErr := s.maybeWrapTLS(ipcListener)
	if tlsErr != nil {
		return tlsErr
	}

	var lc net.ListenConfig

	httpListener, httpErr := lc.Listen(ctx, "tcp", s.on.httpAddr)
	if httpErr != nil {
		return errors.Join(fmt.Errorf("listen on %s: %w", s.on.httpAddr, httpErr), ipcListener.Close())
	}

	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(unaryAuth(token)),
		grpc.ChainStreamInterceptor(streamAuth(token)),
	)
	v1.RegisterControlServer(grpcServer, s.service)

	handler, handlerErr := newHTTPHandler(ctx, &httpDeps{
		store:      s.store,
		data:       s.data,
		metrics:    s.metrics,
		dubbed:     s.service.dubbed,
		ipcTLS:     gatewayTLS,
		log:        s.log,
		token:      token,
		ipcPath:    s.on.ipcPath,
		corsOrigin: s.on.corsOrigin,
		bind:       s.on.httpAddr,
	})
	if handlerErr != nil {
		return errors.Join(handlerErr, ipcListener.Close(), httpListener.Close())
	}
	if afterBind != nil {
		if hookErr := afterBind(); hookErr != nil {
			return errors.Join(hookErr, ipcListener.Close(), httpListener.Close())
		}
	}

	httpServer := newHTTPServer(handler)

	s.log.Info("control plane up",
		"ipc", s.on.ipcPath,
		"dashboard", fmt.Sprintf("http://%s/ui/", s.on.httpAddr),
	)

	return s.serve(ctx, grpcServer, httpServer, ipcListener, httpListener)
}

func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadTimeout:       httpReadTimeout,
		ReadHeaderTimeout: httpHeaderTimeout,
		IdleTimeout:       httpIdleTimeout,
		MaxHeaderBytes:    httpMaxHeaderBytes,
	}
}

func (s *Server) serve(
	ctx context.Context,
	grpcServer *grpc.Server,
	httpServer *http.Server,
	ipcListener net.Listener,
	httpListener net.Listener,
) error {
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return serveGRPC(grpcServer, ipcListener) })
	g.Go(func() error { return serveHTTP(httpServer, httpListener) })
	g.Go(func() error {
		<-gctx.Done()
		stopServers(grpcServer, httpServer, s.log, shutdownTimeout)

		return nil
	})

	return g.Wait()
}

// maybeWrapTLS wraps the IPC listener in TLS when control.ipc_tls is set,
// closing the listener on failure.
func (s *Server) maybeWrapTLS(ipcListener net.Listener) (net.Listener, *tls.Config, error) {
	if !s.on.ipcTLS {
		return ipcListener, nil, nil
	}

	stateDir := filepath.Dir(s.on.tokenPath)

	serverTLS, tlsErr := ServerIPCTLS(stateDir)
	if tlsErr != nil {
		return nil, nil, errors.Join(tlsErr, ipcListener.Close())
	}

	clientTLS, clientErr := ClientIPCTLS(stateDir)
	if clientErr != nil {
		return nil, nil, errors.Join(clientErr, ipcListener.Close())
	}

	return tls.NewListener(ipcListener, serverTLS), clientTLS, nil
}

// serveGRPC adapts grpc.Server.Serve to the errgroup contract.
func serveGRPC(srv *grpc.Server, l net.Listener) error {
	if err := srv.Serve(l); err != nil {
		return fmt.Errorf("grpc serve: %w", err)
	}

	return nil
}

// serveHTTP adapts http.Server.Serve to the errgroup contract, mapping the
// expected post-Shutdown sentinel to a clean exit.
func serveHTTP(srv *http.Server, l net.Listener) error {
	if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http serve: %w", err)
	}

	return nil
}

// stopServers drains both servers; the gRPC drain is time-boxed with a
// hard stop so one connected watcher cannot keep the daemon alive.
func stopServers(grpcServer *grpc.Server, httpServer *http.Server, log *slog.Logger, drain time.Duration) {
	drained := make(chan struct{})

	go func() {
		grpcServer.GracefulStop()
		close(drained)
	}()

	select {
	case <-drained:
	case <-time.After(drain):
		log.Warn("grpc drain timed out; closing open streams")
		grpcServer.Stop()
		<-drained
	}

	drainCtx, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()

	// Shutdown returning an error means connections outlived the drain:
	// Close severs them so the daemon still exits.
	if err := httpServer.Shutdown(drainCtx); err != nil {
		log.Warn("http drain timed out; closing open connections",
			"err", errors.Join(err, httpServer.Close()))
	}
}
