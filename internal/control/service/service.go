// Package service installs, removes and inspects the OS service wrapping
// the daemon: systemd, launchd or SCM.
package service

import "context"

// Name is the service identity across all three platforms.
const Name = "prukka"

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
