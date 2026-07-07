//go:build windows

package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// stopWait bounds how long removal waits for a running service to stop.
const stopWait = 10 * time.Second

// install registers the daemon with the SCM; handle-release errors fold
// into the named result.
func install(_ context.Context, opts *Options) (err error) {
	m, connectErr := mgr.Connect()
	if connectErr != nil {
		return fmt.Errorf("connect to service manager: %w (Administrator required)", connectErr)
	}
	defer func() { err = errors.Join(err, m.Disconnect()) }()

	args := daemonArgs(opts)

	s, createErr := m.CreateService(Name, opts.ExecPath, mgr.Config{
		DisplayName: "Prukka Daemon",
		Description: "Real-time multilingual dubbing and interpretation engine.",
		StartType:   mgr.StartAutomatic,
	}, args[1:]...)
	if createErr != nil {
		return fmt.Errorf("create service: %w", createErr)
	}
	defer func() { err = errors.Join(err, s.Close()) }()

	if opts.Now {
		if startErr := s.Start(); startErr != nil {
			return fmt.Errorf("start service: %w", startErr)
		}
	}

	return nil
}

// remove stops and deletes the service; removing an uninstalled service
// succeeds.
func remove(_ context.Context) (err error) {
	m, connectErr := mgr.Connect()
	if connectErr != nil {
		return fmt.Errorf("connect to service manager: %w (Administrator required)", connectErr)
	}
	defer func() { err = errors.Join(err, m.Disconnect()) }()

	s, openErr := m.OpenService(Name)
	if openErr != nil {
		return nil
	}
	defer func() { err = errors.Join(err, s.Close()) }()

	stopService(s)

	if deleteErr := s.Delete(); deleteErr != nil {
		return fmt.Errorf("delete service: %w", deleteErr)
	}

	return nil
}

// status reports the SCM's view of the service.
func status(_ context.Context) (state string, err error) {
	m, connectErr := mgr.Connect()
	if connectErr != nil {
		return "", fmt.Errorf("connect to service manager: %w", connectErr)
	}
	defer func() { err = errors.Join(err, m.Disconnect()) }()

	s, openErr := m.OpenService(Name)
	if openErr != nil {
		return "not installed", nil
	}
	defer func() { err = errors.Join(err, s.Close()) }()

	q, queryErr := s.Query()
	if queryErr != nil {
		return "", fmt.Errorf("query service: %w", queryErr)
	}

	if q.State == svc.Running {
		return "running", nil
	}

	return "installed (not running)", nil
}

// rendered explains that Windows keeps service definitions in the SCM
// database rather than a file.
func rendered(opts *Options) (path, content string, err error) {
	content = fmt.Sprintf(
		"Windows stores the definition in the service control manager, not a file.\n"+
			"Service %q would run: %v (start type: automatic)\n", Name, daemonArgs(opts))

	return "(service control manager)", content, nil
}

// stopService asks a running service to stop and waits briefly; a service
// that was not running is not an error.
func stopService(s *mgr.Service) {
	stopStatus, err := s.Control(svc.Stop)
	if err != nil {
		return
	}

	deadline := time.Now().Add(stopWait)
	for stopStatus.State != svc.Stopped && time.Now().Before(deadline) {
		time.Sleep(time.Second)

		stopStatus, err = s.Query()
		if err != nil {
			return
		}
	}
}
