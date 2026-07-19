//go:build darwin

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ubyte-source/prukka/internal/procio"
)

// label identifies the daemon agent in launchd.
const label = "io.prukka.daemon"

// The daemon must run as the logged-in user: the control socket and its
// token file sit in the user's state directory, invisible to a root
// daemon. A per-user launch agent is therefore the only launchd flavor
// that works.
const errRoot = "the macOS service is a per-user launch agent — run `prukka service` commands without sudo"

// rootGuard refuses every service verb under sudo: launchctl would
// resolve gui/$UID to root's (empty) domain and silently act on the
// wrong agent.
func rootGuard() error {
	if os.Geteuid() == 0 {
		return errors.New(errRoot)
	}

	return nil
}

// bootstrapAttempts bounds the bootstrap retries; launchctl reports an
// I/O error while a booted-out instance is still tearing down, and
// Background Task Management re-registers the item for several seconds
// after the binary behind it is replaced (a fresh install over a live
// agent hits exactly that window).
const bootstrapAttempts = 20

// bootstrapRetryDelay paces the bootstrap retries; a variable so tests
// do not sleep.
var bootstrapRetryDelay = 500 * time.Millisecond

// agentTarget is the launchctl domain/label pair of the per-user agent.
func agentTarget() string {
	return fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
}

// agentPlistPath is where the launch-agent definition lands.
func agentPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}

	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

// xmlEscaper covers the five XML special characters.
var xmlEscaper = strings.NewReplacer(
	"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;",
)

// xmlEscape makes a value safe inside the plist definition.
func xmlEscape(s string) string {
	return xmlEscaper.Replace(s)
}

// daemonLogPath is where launchd captures the daemon's output — without it
// every log line vanishes and field problems cannot be diagnosed. The
// user's Logs folder keeps it visible in Console.app.
func daemonLogPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}

	return filepath.Join(home, "Library", "Logs", "Prukka", "daemon.log"), nil
}

// install writes the plist and bootstraps it into the user's gui domain.
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

	// launchd creates the log file itself but not its directory.
	logPath, logErr := daemonLogPath()
	if logErr != nil {
		return logErr
	}

	if mkErr := os.MkdirAll(filepath.Dir(logPath), 0o750); mkErr != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(logPath), mkErr)
	}

	// launchd refuses group/world-writable plists; 0600 satisfies that
	// and the linter's permission rule.
	if writeErr := os.WriteFile(path, []byte(content), 0o600); writeErr != nil {
		return fmt.Errorf("write %s: %w", path, writeErr)
	}

	if !opts.Now {
		return nil
	}

	// A live instance makes bootstrap fail with "already bootstrapped";
	// boot it out first. That error only matters if bootstrap also fails.
	bootoutErr := procio.RunQuiet(exec.CommandContext(ctx, "launchctl", "bootout", agentTarget()))

	if bootstrapErr := bootstrap(ctx, path); bootstrapErr != nil {
		return errors.Join(bootstrapErr, bootoutErr)
	}

	return nil
}

// bootstrap loads the agent, retrying while launchd settles after a
// bootout of the previous instance; a permanent failure (wrong domain,
// missing plist, already bootstrapped) surfaces immediately.
func bootstrap(ctx context.Context, path string) error {
	domain := fmt.Sprintf("gui/%d", os.Getuid())

	var err error

	for range bootstrapAttempts {
		if err = procio.RunQuiet(exec.CommandContext(ctx, "launchctl", "bootstrap", domain, path)); err == nil {
			return nil
		}

		if !transientBootstrap(err) {
			return err
		}

		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(bootstrapRetryDelay):
		}
	}

	return err
}

// transientBootstrap recognizes the EIO launchd reports while a
// booted-out instance is still tearing down (and for several seconds
// after Background Task Management sees the binary change) — the only
// bootstrap failure worth retrying.
func transientBootstrap(err error) bool {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode() == int(syscall.EIO)
	}

	return false
}

// restart relaunches the daemon; a service that was installed but never
// bootstrapped gets bootstrapped instead. Without a plist there is
// nothing to fall back to — say so instead of retrying into the void.
func restart(ctx context.Context) error {
	if err := rootGuard(); err != nil {
		return err
	}

	kickErr := procio.RunQuiet(exec.CommandContext(ctx, "launchctl", "kickstart", "-k", agentTarget()))
	if kickErr == nil {
		return nil
	}

	path, pathErr := agentPlistPath()
	if pathErr != nil {
		return errors.Join(kickErr, pathErr)
	}

	if _, statErr := os.Stat(path); statErr != nil {
		return errors.Join(kickErr, fmt.Errorf("service not installed — run `prukka service install`: %w", statErr))
	}

	if bootstrapErr := bootstrap(ctx, path); bootstrapErr != nil {
		return errors.Join(kickErr, bootstrapErr)
	}

	return nil
}

// remove boots the daemon out of launchd and deletes the plist; removing
// an uninstalled service succeeds.
func remove(ctx context.Context) error {
	if err := rootGuard(); err != nil {
		return err
	}

	path, err := agentPlistPath()
	if err != nil {
		return err
	}

	// Bootout runs even without a plist on disk: a loaded agent whose
	// plist was deleted by hand must still be stopped. It fails when the
	// agent was never bootstrapped; that alone is fine, and it is only
	// surfaced if the removal also fails.
	bootoutErr := procio.RunQuiet(exec.CommandContext(ctx, "launchctl", "bootout", agentTarget()))

	if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
		return errors.Join(fmt.Errorf("remove %s: %w", path, rmErr), bootoutErr)
	}
	if bootoutErr != nil {
		// A missing agent makes bootout fail harmlessly. If launchd can still
		// print the target, however, deleting the plist did not stop it.
		if printErr := procio.RunQuiet(exec.CommandContext(ctx, "launchctl", "print", agentTarget())); printErr == nil {
			return bootoutErr
		}
	}

	return nil
}

// status reports launchd's view of the daemon.
func status(ctx context.Context) (string, error) {
	if err := rootGuard(); err != nil {
		return "", err
	}

	out, err := exec.CommandContext(ctx, "launchctl", "print", agentTarget()).CombinedOutput()
	if err != nil {
		path, pathErr := agentPlistPath()
		if pathErr != nil {
			return "", pathErr
		}

		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			return "not installed", nil
		}

		return "installed (not running)", nil
	}

	for line := range strings.Lines(string(out)) {
		if strings.Contains(line, "state =") {
			return strings.TrimSpace(strings.SplitN(line, "=", 2)[1]), nil
		}
	}

	return "running", nil
}

// rendered produces the plist content.
func rendered(opts *Options) (path, content string, err error) {
	path, err = agentPlistPath()
	if err != nil {
		return "", "", err
	}

	logPath, err := daemonLogPath()
	if err != nil {
		return "", "", err
	}

	args := daemonArgs(opts)

	var xmlArgs strings.Builder
	for _, a := range args {
		xmlArgs.WriteString("        <string>" + xmlEscape(a) + "</string>\n")
	}

	content = fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>io.prukka.daemon</string>
    <key>ProgramArguments</key>
    <array>
%s    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>
    <key>StandardOutPath</key>
    <string>%s</string>
    <key>StandardErrorPath</key>
    <string>%s</string>
</dict>
</plist>
`, xmlArgs.String(), xmlEscape(logPath), xmlEscape(logPath))

	return path, content, nil
}
