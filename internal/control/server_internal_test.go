package control

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/config"
	"github.com/ubyte-source/prukka/internal/core/session"

	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

// TestStopServersEndsOpenEventStreams: a connected watcher holds a plain
// graceful stop forever, so stopServers must hard-close it in time.
func TestStopServersEndsOpenEventStreams(t *testing.T) {
	t.Parallel()

	grpcServer, stream := serveWatchedStream(t)

	// Premise — the old shutdown: graceful alone never returns while the
	// watcher is connected.
	premise := make(chan struct{})

	go func() { grpcServer.GracefulStop(); close(premise) }()

	select {
	case <-premise:
		t.Fatal("GracefulStop returned with an open stream; the time-box would be pointless")
	case <-time.After(500 * time.Millisecond):
	}

	// The fix: stopServers gives up gracefully after the drain budget.
	done := make(chan struct{})

	go func() {
		httpServer := &http.Server{ReadHeaderTimeout: time.Second}
		stopServers(grpcServer, httpServer, slog.New(slog.DiscardHandler), 300*time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stopServers never returned: an open watcher still blocks shutdown")
	}

	if _, err := stream.Recv(); err == nil {
		t.Fatal("the watcher's stream survived the hard stop")
	}
}

// serveWatchedStream boots a control service on a loopback listener and
// returns it with one provenly-active StreamEvents watcher.
func serveWatchedStream(t *testing.T) (*grpc.Server, grpc.ServerStreamingClient[v1.StreamEventsResponse]) {
	t.Helper()

	store := session.NewStore()
	grpcServer := grpc.NewServer()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if writeErr := os.WriteFile(path, nil, 0o600); writeErr != nil {
		t.Fatalf("seed config: %v", writeErr)
	}

	holder, err := config.NewHolder(path)
	if err != nil {
		t.Fatalf("NewHolder returned error: %v", err)
	}

	v1.RegisterControlServer(grpcServer, NewService(store, "test", nil, nil, nil, nil, NewSettings(holder, nil)))

	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	go func() {
		if serveErr := grpcServer.Serve(listener); serveErr != nil {
			t.Logf("serve: %v", serveErr)
		}
	}()

	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	t.Cleanup(func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Logf("conn close: %v", closeErr)
		}
	})

	stream, err := v1.NewControlClient(conn).StreamEvents(context.Background(), &v1.StreamEventsRequest{})
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}

	proveStreamLive(t, store, stream)

	return grpcServer, stream
}

// proveStreamLive round-trips an event through the watcher so the drain
// cannot win the race against stream setup.
func proveStreamLive(
	t *testing.T, store *session.Store, stream grpc.ServerStreamingClient[v1.StreamEventsResponse],
) {
	t.Helper()

	received := make(chan error, 1)

	go func() {
		_, recvErr := stream.Recv()
		received <- recvErr
	}()

	for i := 0; ; i++ {
		if i == 50 {
			t.Fatal("the event stream never became live")
		}

		if createErr := store.Create(&session.Session{
			Slug:    fmt.Sprintf("watched-%d", i),
			Profile: session.ProfileBroadcast,
			Source:  core.SourceSpec{URL: "file:///tmp/x.wav"},
			Langs:   []core.Lang{"it", "en"},
		}); createErr != nil {
			t.Fatalf("create session: %v", createErr)
		}

		select {
		case recvErr := <-received:
			if recvErr != nil {
				t.Fatalf("receive first event: %v", recvErr)
			}

			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}
