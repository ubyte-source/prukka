//go:build windows

package control

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestListenAndDialIPCRoundTrip: the daemon listens on a named pipe and a
// client dials it — one byte each way proves the transport.
func TestListenAndDialIPCRoundTrip(t *testing.T) {
	t.Parallel()

	path := fmt.Sprintf(`\\.\pipe\prukka-test-%d`, time.Now().UnixNano())

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
