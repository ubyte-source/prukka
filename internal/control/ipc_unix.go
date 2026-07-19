//go:build !windows

package control

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"time"
)

// ipcProbeTimeout bounds the liveness probe on a leftover socket.
const ipcProbeTimeout = time.Second

// listenIPC binds the local control endpoint: a UNIX socket in a 0750
// directory, tightened to 0600.
func listenIPC(ctx context.Context, path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}

	if err := clearStaleSocket(ctx, path); err != nil {
		return nil, err
	}

	var lc net.ListenConfig

	l, err := lc.Listen(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on control socket: %w", err)
	}

	if chmodErr := os.Chmod(path, 0o600); chmodErr != nil {
		return nil, errors.Join(fmt.Errorf("restrict control socket: %w", chmodErr), l.Close())
	}

	return l, nil
}

// clearStaleSocket removes a socket left by an unclean shutdown, but refuses
// to steal one a live daemon still answers on.
func clearStaleSocket(ctx context.Context, path string) error {
	d := net.Dialer{Timeout: ipcProbeTimeout}

	conn, dialErr := d.DialContext(ctx, "unix", path)
	if dialErr == nil {
		return errors.Join(fmt.Errorf("another prukka daemon is already running (socket %s)", path), conn.Close())
	}

	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove stale socket: %w", err)
	}

	return nil
}

// dialIPC connects a client to the daemon's local endpoint.
func dialIPC(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer

	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("dial control socket: %w", err)
	}

	return conn, nil
}
