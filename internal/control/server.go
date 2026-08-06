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
	"github.com/ubyte-source/prukka/internal/paths"

	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

// httpHeaderTimeout bounds header reads so slow clients cannot pin the listener.
const httpHeaderTimeout = 10 * time.Second

// httpReadTimeout also bounds request bodies; SSE and media are write-side, so
// it does not cut them.
const httpReadTimeout = 15 * time.Second

// httpIdleTimeout releases inactive keep-alive connections.
const httpIdleTimeout = 60 * time.Second

// httpMaxHeaderBytes replaces net/http's default megabyte per connection.
const httpMaxHeaderBytes = 64 << 10

// shutdownTimeout bounds the graceful HTTP drain on shutdown.
const shutdownTimeout = 5 * time.Second

// DataPlane bundles the read ports the HTTP surface serves media from.
type DataPlane struct {
	Docs    CaptionDocs
	Streams AudioStreams
	Media   MediaTree
	Log     *slog.Logger
}

// endpoints holds where the daemon listens and how its surfaces are guarded.
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

// ServerDeps carries the control-plane server's collaborators; Metrics may be
// nil to omit /metrics.
type ServerDeps struct {
	Config  *config.Config
	Store   *session.Store
	Service *Service
	Data    DataPlane
	Metrics http.Handler
	Log     *slog.Logger
}

// NewServer wires a control-plane server.
func NewServer(deps *ServerDeps) *Server {
	return &Server{
		log:     deps.Log,
		store:   deps.Store,
		service: deps.Service,
		data:    deps.Data,
		metrics: deps.Metrics,
		on: endpoints{
			httpAddr:   deps.Config.Daemon.HTTP,
			ipcPath:    paths.IPCPath(),
			tokenPath:  paths.TokenPath(),
			corsOrigin: deps.Config.Daemon.CORSOrigin,
			ipcTLS:     deps.Config.Control.IPCTLS,
		},
	}
}

// RunAfterBind reserves both control endpoints, then runs afterBind before
// either can serve; a failing hook closes both listeners.
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
		engine:     s.service.engine,
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
		s.stopEngine()

		return nil
	})

	return g.Wait()
}

// stopEngine cancels and joins the engine's asynchronous install once no
// server can accept another, so a restart inherits no stale install lock.
func (s *Server) stopEngine() {
	engine := s.service.engine
	if engine == nil {
		return
	}

	drainCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := engine.Shutdown(drainCtx); err != nil {
		s.log.Warn("engine drain timed out; abandoning the in-flight operation", "err", err)
	}
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
// post-Shutdown sentinel to a clean exit.
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

	// Connections that outlived the drain are severed so the daemon exits.
	if err := httpServer.Shutdown(drainCtx); err != nil {
		log.Warn("http drain timed out; closing open connections",
			"err", errors.Join(err, httpServer.Close()))
	}
}
