//go:build linux

package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// unitPath is where the systemd unit lands.
const unitPath = "/etc/systemd/system/prukka.service"

// install writes the unit and reloads systemd.
func install(ctx context.Context, opts *Options) error {
	_, content, err := rendered(opts)
	if err != nil {
		return err
	}

	// 0600 satisfies both systemd (which reads as root) and the linter's
	// file-permission rule.
	if writeErr := os.WriteFile(unitPath, []byte(content), 0o600); writeErr != nil {
		return fmt.Errorf("write %s: %w (root required — try sudo)", unitPath, writeErr)
	}

	if reloadErr := runQuiet(exec.CommandContext(ctx, "systemctl", "daemon-reload")); reloadErr != nil {
		return reloadErr
	}

	if opts.Now {
		return runQuiet(exec.CommandContext(ctx, "systemctl", "enable", "--now", "prukka.service"))
	}

	return runQuiet(exec.CommandContext(ctx, "systemctl", "enable", "prukka.service"))
}

// remove disables the unit and deletes it; removing an uninstalled service
// succeeds.
func remove(ctx context.Context) error {
	if _, err := os.Stat(unitPath); os.IsNotExist(err) {
		return nil
	}

	if err := runQuiet(exec.CommandContext(ctx, "systemctl", "disable", "--now", "prukka.service")); err != nil {
		return err
	}

	if err := os.Remove(unitPath); err != nil {
		return fmt.Errorf("remove %s: %w", unitPath, err)
	}

	return runQuiet(exec.CommandContext(ctx, "systemctl", "daemon-reload"))
}

// status reports systemd's view of the unit.
func status(ctx context.Context) (string, error) {
	// is-active exits nonzero for inactive units while still printing the
	// state, so the output wins over the exit code here.
	out, err := exec.CommandContext(ctx, "systemctl", "is-active", "prukka.service").CombinedOutput()

	state := strings.TrimSpace(string(out))
	if state == "" && err != nil {
		return "", fmt.Errorf("query systemd: %w", err)
	}

	return state, nil
}

// rendered produces the unit file content.
func rendered(opts *Options) (path, content string, err error) {
	exe := strings.Join(daemonArgs(opts), " ")

	content = fmt.Sprintf(`[Unit]
Description=Prukka real-time dubbing daemon
Documentation=https://github.com/ubyte-source/prukka
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
`, exe)

	return unitPath, content, nil
}

// runQuiet runs a prepared command, folding its output into any error.
func runQuiet(cmd *exec.Cmd) error {
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", strings.Join(cmd.Args, " "), err, strings.TrimSpace(string(out)))
	}

	return nil
}
