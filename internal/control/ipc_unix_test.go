//go:build !windows

package control

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// shortSocketPath returns a socket path directly under /tmp: t.TempDir is
// too deep for the kernel's sun_path limit (104 bytes on macOS).
func shortSocketPath(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("/tmp", "ipc")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}

	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})

	return filepath.Join(dir, "c.sock")
}

// TestListenAndDialIPCRoundTrip: the daemon listens on a UNIX socket and a
// client dials it — one byte each way proves the transport.
func TestListenAndDialIPCRoundTrip(t *testing.T) {
	t.Parallel()

	path := shortSocketPath(t)

	listener, err := listenIPC(t.Context(), path)
	if err != nil {
		t.Fatalf("listenIPC: %v", err)
	}
	defer func() {
		if closeErr := listener.Close(); closeErr != nil {
			t.Logf("close listener: %v", closeErr)
		}
	}()

	done := make(chan error, 1)

	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			done <- acceptErr

			return
		}
		defer func() {
			if closeErr := conn.Close(); closeErr != nil {
				t.Logf("close conn: %v", closeErr)
			}
		}()

		buf := make([]byte, 1)
		_, readErr := conn.Read(buf)
		done <- readErr
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := dialIPC(ctx, path)
	if err != nil {
		t.Fatalf("dialIPC: %v", err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Logf("close conn: %v", closeErr)
		}
	}()

	if _, err := conn.Write([]byte{1}); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := <-done; err != nil {
		t.Fatalf("server side: %v", err)
	}
}

// TestListenIPCRefusesALiveDaemon: a second listener on a socket someone
// answers must fail with the daemon-running sentinel, not steal it.
func TestListenIPCRefusesALiveDaemon(t *testing.T) {
	t.Parallel()

	path := shortSocketPath(t)

	first, err := listenIPC(t.Context(), path)
	if err != nil {
		t.Fatalf("first listenIPC: %v", err)
	}
	defer func() {
		if closeErr := first.Close(); closeErr != nil {
			t.Logf("close first: %v", closeErr)
		}
	}()

	go func() {
		for {
			conn, acceptErr := first.Accept()
			if acceptErr != nil {
				return
			}

			if err := conn.Close(); err != nil {
				return
			}
		}
	}()

	if _, err := listenIPC(t.Context(), path); err == nil {
		t.Fatal("a second daemon claimed a live socket")
	}
}

// TestListenIPCClearsAStaleSocket: a socket file with nobody behind it is
// swept so a crashed daemon does not block the next start.
func TestListenIPCClearsAStaleSocket(t *testing.T) {
	t.Parallel()

	path := shortSocketPath(t)

	var lc net.ListenConfig

	stale, err := lc.Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatalf("stale listener: %v", err)
	}
	// Close without unlinking: the file stays, nobody answers.
	if closeErr := stale.Close(); closeErr != nil {
		t.Fatalf("close stale: %v", closeErr)
	}

	fresh, err := listenIPC(t.Context(), path)
	if err != nil {
		t.Fatalf("listenIPC over a stale socket: %v", err)
	}

	if closeErr := fresh.Close(); closeErr != nil {
		t.Fatalf("close fresh: %v", closeErr)
	}
}
