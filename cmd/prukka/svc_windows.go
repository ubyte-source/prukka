//go:build windows

package main

import (
	"context"
	"fmt"

	"golang.org/x/sys/windows/svc"
)

// runService runs the daemon under the service control manager when started
// as a Windows service, or directly when started interactively.
func runService(ctx context.Context, run func(context.Context) error) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return fmt.Errorf("detect service context: %w", err)
	}

	if !isService {
		return run(ctx)
	}

	if svcErr := svc.Run("prukka", &scmHandler{ctx: ctx, run: run}); svcErr != nil {
		return fmt.Errorf("run as windows service: %w", svcErr)
	}

	return nil
}

// scmHandler adapts the daemon lifecycle to SCM change requests.
type scmHandler struct {
	ctx context.Context
	run func(context.Context) error
}

// Execute implements svc.Handler.
func (h *scmHandler) Execute(
	_ []string, requests <-chan svc.ChangeRequest, statuses chan<- svc.Status,
) (svcSpecific bool, exitCode uint32) {
	statuses <- svc.Status{State: svc.StartPending}

	runCtx, cancel := context.WithCancel(h.ctx)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- h.run(runCtx) }()

	statuses <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for {
		select {
		case req := <-requests:
			switch req.Cmd {
			case svc.Stop, svc.Shutdown:
				statuses <- svc.Status{State: svc.StopPending}
				cancel()
				<-done

				return false, 0
			case svc.Interrogate:
				statuses <- req.CurrentStatus
			default: // other commands are not accepted (see Accepts above)
			}
		case err := <-done:
			if err != nil {
				return false, 1
			}

			return false, 0
		}
	}
}
