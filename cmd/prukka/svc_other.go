//go:build !windows

package main

import "context"

// runService executes run directly: only Windows wraps the daemon in a
// service-control-manager handler (see svc_windows.go).
func runService(ctx context.Context, run func(context.Context) error) error {
	return run(ctx)
}
