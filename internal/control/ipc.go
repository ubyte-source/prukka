package control

import "errors"

// ErrDaemonRunning reports that a live daemon already owns the local control
// endpoint.
var ErrDaemonRunning = errors.New("another prukka daemon is already running")

// Only the divergent transport layer — UNIX sockets versus Windows named
// pipes — is per-OS, behind listenIPC/dialIPC.
