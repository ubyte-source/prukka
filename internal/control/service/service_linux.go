//go:build linux

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// unit is the systemd unit name of the daemon.
const unit = "prukka.service"

// The daemon must run as the logged-in user: the control socket lives in
// the user's runtime directory and its token file in the user's state
// directory, both unreachable from a root unit. A per-user systemd unit
// is therefore the only flavor that works.
const errRoot = "the Linux service is a per-user systemd unit — run `prukka service install` without sudo"

// unitPath is where the user unit lands.
func unitPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate config directory: %w", err)
	}

	return filepath.Join(dir, "systemd", "user", unit), nil
}

// install writes the user unit and enables it.
func install(ctx context.Context, opts *Options) error {
	if os.Geteuid() == 0 {
		return errors.New(errRoot)
	}

	path, content, err := rendered(opts)
	if err != nil {
		return err
	}

	if mkErr := os.MkdirAll(filepath.Dir(path), 0o700); mkErr != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), mkErr)
	}

	if writeErr := os.WriteFile(path, []byte(content), 0o600); writeErr != nil {
		return fmt.Errorf("write %s: %w", path, writeErr)
	}

	if reloadErr := runQuiet(exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload")); reloadErr != nil {
		return reloadErr
	}

	if opts.Now {
		return runQuiet(exec.CommandContext(ctx, "systemctl", "--user", "enable", "--now", unit))
	}

	return runQuiet(exec.CommandContext(ctx, "systemctl", "--user", "enable", unit))
}

// remove stops the manager's loaded unit even when its definition was
// deleted by hand, then removes both the definition and enablement link.
func remove(ctx context.Context) error {
	path, err := unitPath()
	if err != nil {
		return err
	}

	disableErr := runQuiet(exec.CommandContext(ctx, "systemctl", "--user", "disable", "--now", unit))

	if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
		return fmt.Errorf("remove %s: %w", path, rmErr)
	}
	wants := filepath.Join(filepath.Dir(path), "default.target.wants", unit)
	if rmErr := os.Remove(wants); rmErr != nil && !os.IsNotExist(rmErr) {
		return fmt.Errorf("remove %s: %w", wants, rmErr)
	}

	if reloadErr := runQuiet(exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload")); reloadErr != nil {
		return errors.Join(reloadErr, disableErr)
	}
	if disableErr != nil {
		state, statusErr := status(ctx)
		if statusErr != nil {
			return errors.Join(disableErr, statusErr)
		}
		if Running(state) {
			return disableErr
		}
	}

	return nil
}

// restart relaunches the daemon through the user manager.
func restart(ctx context.Context) error {
	return runQuiet(exec.CommandContext(ctx, "systemctl", "--user", "restart", unit))
}

// status reports the user manager's view of the unit. systemd is asked
// first — a unit can be live with its file already gone, and skipping the
// probe would leave such a daemon unrestarted after an update; the file
// check only refines a dead answer into "not installed".
func status(ctx context.Context) (string, error) {
	// is-active exits nonzero for inactive units while still printing the
	// state, so the output wins over the exit code here.
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "is-active", unit).CombinedOutput()

	state := strings.TrimSpace(string(out))
	if state == "" && err != nil {
		return "", fmt.Errorf("query systemd: %w", err)
	}

	if state == "active" {
		return state, nil
	}

	path, pathErr := unitPath()
	if pathErr != nil {
		return "", pathErr
	}

	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		return "not installed", nil
	}

	return state, nil
}

// systemdArg renders one ExecStart= argument: a literal % would start a
// unit specifier, a literal $ a variable expansion, and whitespace needs
// double quotes (with inner backslashes and quotes escaped).
func systemdArg(a string) string {
	a = strings.ReplaceAll(a, "%", "%%")
	a = strings.ReplaceAll(a, "$", "$$")

	if !strings.ContainsAny(a, " \t") {
		return a
	}

	a = strings.ReplaceAll(a, `\`, `\\`)

	return `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
}

// rendered produces the user unit content.
func rendered(opts *Options) (path, content string, err error) {
	path, err = unitPath()
	if err != nil {
		return "", "", err
	}

	args := daemonArgs(opts)

	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = systemdArg(a)
	}

	exe := strings.Join(quoted, " ")

	content = fmt.Sprintf(`[Unit]
Description=Prukka real-time dubbing daemon
Documentation=https://github.com/ubyte-source/prukka

[Service]
ExecStart=%s
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
`, exe)

	return path, content, nil
}

// runQuiet runs a prepared command, folding its output into any error.
func runQuiet(cmd *exec.Cmd) error {
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", strings.Join(cmd.Args, " "), err, strings.TrimSpace(string(out)))
	}

	return nil
}
