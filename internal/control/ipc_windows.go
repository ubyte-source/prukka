//go:build windows

package control

import (
	"context"
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
)

// pipeSecurity is the SDDL granting pipe access to SYSTEM, Administrators and
// the owner.
const pipeSecurity = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;OW)"

// listenIPC binds the named-pipe control endpoint; winio's listener takes no
// context.
func listenIPC(_ context.Context, path string) (net.Listener, error) {
	l, err := winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: pipeSecurity})
	if err != nil {
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
