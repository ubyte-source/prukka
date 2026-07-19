//go:build windows

package control

import (
	"context"
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
)

// pipeSecurity grants pipe access to SYSTEM, Administrators and the owner
// (SDDL ACL for Administrators plus the interactive owner).
const pipeSecurity = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;OW)"

// listenIPC binds the named-pipe control endpoint. The context parameter
// mirrors the UNIX implementation; winio's listener takes none.
func listenIPC(_ context.Context, path string) (net.Listener, error) {
	l, err := winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: pipeSecurity})
	if err != nil {
		// A pipe another process owns reports access-denied; surface the
		// raw error rather than guess.
		return nil, fmt.Errorf("listen on control pipe: %w", err)
	}

	return l, nil
}

// dialIPC connects a client to the daemon's named pipe.
func dialIPC(ctx context.Context, path string) (net.Conn, error) {
	conn, err := winio.DialPipeContext(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("dial control pipe: %w", err)
	}

	return conn, nil
}
