//go:build linux

package osservice

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ubyte-source/prukka/internal/procio"
)

// unit is the systemd unit name of the daemon.
const unit = "prukka.service"

// The daemon must run as the logged-in user: the control socket lives in the
// user's runtime directory and its token file in the user's state directory,
// both unreachable from a root unit.
const errRoot = "the Linux service is a per-user systemd unit — run `prukka service` commands without sudo"

// euid is a variable so tests can exercise the guard without running as root.
var euid = os.Geteuid

// rootGuard refuses every service verb under sudo: systemctl --user and
// unitPath would resolve from root's identity and act on the wrong service.
func rootGuard() error {
	if euid() == 0 {
		return errors.New(errRoot)
	}

	return nil
}

// unitPath is where the user unit lands.
func unitPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate config directory: %w", err)
	}

	return filepath.Join(dir, "systemd", "user", unit), nil
}

// install writes the user unit, enables it and, with --now, replaces any
// live instance.
func install(ctx context.Context, opts *Options) error {
	if err := rootGuard(); err != nil {
		return err
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

	if reloadErr := procio.RunQuiet(exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload")); reloadErr != nil {
		return reloadErr
	}

	if enableErr := procio.RunQuiet(exec.CommandContext(ctx, "systemctl", "--user", "enable", unit)); enableErr != nil {
		return enableErr
	}

	if !opts.Now {
		return nil
	}

	// The `--now` of `enable --now` is only a start, which systemd treats as a
	// no-op on an already-active unit: a reinstall over a live daemon would
	// keep running the previous ExecStart and binary image until the next
	// logout. restart starts a dead unit and replaces a live one.
	return restart(ctx)
}

// remove stops the manager's loaded unit even when its definition was deleted
// by hand, then removes both the definition and enablement link.
func remove(ctx context.Context) error {
	if err := rootGuard(); err != nil {
		return err
	}

	path, err := unitPath()
	if err != nil {
		return err
	}

	disableErr := procio.RunQuiet(exec.CommandContext(ctx, "systemctl", "--user", "disable", "--now", unit))

	if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, rmErr)
	}
	wants := filepath.Join(filepath.Dir(path), "default.target.wants", unit)
	if rmErr := os.Remove(wants); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", wants, rmErr)
	}

	if reloadErr := procio.RunQuiet(exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload")); reloadErr != nil {
		return errors.Join(reloadErr, disableErr)
	}

	return disableOutcome(ctx, disableErr)
}

// disableOutcome resolves a failed disable: systemd also refuses the verb for
// a unit that was never loaded, harmless exactly when none is left running.
func disableOutcome(ctx context.Context, disableErr error) error {
	if disableErr == nil {
		return nil
	}

	state, statusErr := status(ctx)
	if statusErr != nil {
		return errors.Join(disableErr, statusErr)
	}
	if Running(state) {
		return disableErr
	}

	return nil
}

// restart relaunches the daemon through the user manager.
func restart(ctx context.Context) error {
	if err := rootGuard(); err != nil {
		return err
	}

	return procio.RunQuiet(exec.CommandContext(ctx, "systemctl", "--user", "restart", unit))
}

// status reports the user manager's view of the unit; systemd is asked first
// because a unit can be live with its file already gone.
func status(ctx context.Context) (string, error) {
	if err := rootGuard(); err != nil {
		return "", err
	}

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

	if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
		return "not installed", nil
	}

	return state, nil
}

// systemdArg renders one ExecStart= argument: a literal % would start a unit
// specifier, a literal $ a variable expansion, and whitespace needs quotes.
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
