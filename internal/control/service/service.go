// Package service installs, removes and inspects the per-user service
// wrapping the daemon: a launchd agent, a systemd user unit or a Windows
// scheduled logon task.
package service

import (
	"context"
	"os"
)

// RestartHint is the command that relaunches the daemon service, spelled
// with the running binary's own path — right after an install PATH rarely
// carries it yet.
func RestartHint() string {
	exe, err := os.Executable()
	if err != nil {
		return "prukka service restart"
	}

	return exe + " service restart"
}

// Options configures installation.
type Options struct {
	// ExecPath is the absolute path of the prukka binary to run.
	ExecPath string
	// ConfigPath optionally pins --config for the daemon.
	ConfigPath string
	// Now also enables and starts the service right after installing.
	Now bool
}

// Install registers the daemon with the platform service manager.
func Install(ctx context.Context, opts *Options) error {
	return install(ctx, opts)
}

// Remove stops and unregisters the daemon service.
func Remove(ctx context.Context) error {
	return remove(ctx)
}

// Restart relaunches the daemon service so it picks up a replaced binary
// or configuration.
func Restart(ctx context.Context) error {
	return restart(ctx)
}

// Running reports whether a Status value describes a live daemon:
// launchd and Windows say "running", systemd says "active".
func Running(state string) bool {
	return state == "running" || state == "active"
}

// Status reports the platform service manager's view of the daemon.
func Status(ctx context.Context) (string, error) {
	return status(ctx)
}

// Rendered returns the definition Install would write and where
// (`prukka service install --print`).
func Rendered(opts *Options) (path, content string, err error) {
	return rendered(opts)
}

// daemonArgs builds the daemon invocation shared by every platform.
func daemonArgs(opts *Options) []string {
	args := []string{opts.ExecPath, "daemon"}
	if opts.ConfigPath != "" {
		args = append(args, "--config", opts.ConfigPath)
	}

	return args
}
