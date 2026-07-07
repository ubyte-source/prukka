//go:build darwin

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// plistPath is where the launchd daemon definition lands.
const plistPath = "/Library/LaunchDaemons/io.prukka.daemon.plist"

// serviceTarget is the launchctl domain/label pair of the daemon.
const serviceTarget = "system/io.prukka.daemon"

// install writes the plist and bootstraps it into the system domain.
func install(ctx context.Context, opts *Options) error {
	_, content, err := rendered(opts)
	if err != nil {
		return err
	}

	// launchd requires the plist to be root-owned and not group/world
	// writable; 0600 satisfies that and the linter's permission rule.
	if writeErr := os.WriteFile(plistPath, []byte(content), 0o600); writeErr != nil {
		return fmt.Errorf("write %s: %w (root required — try sudo)", plistPath, writeErr)
	}

	if !opts.Now {
		return nil
	}

	return runQuiet(exec.CommandContext(ctx, "launchctl", "bootstrap", "system", plistPath))
}

// remove boots the daemon out of launchd and deletes the plist; removing an
// uninstalled service succeeds.
func remove(ctx context.Context) error {
	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		return nil
	}

	// Bootout fails when the plist was written but never bootstrapped; that
	// alone is fine. It is only surfaced if the removal also fails.
	bootoutErr := runQuiet(exec.CommandContext(ctx, "launchctl", "bootout", serviceTarget))

	if err := os.Remove(plistPath); err != nil {
		return errors.Join(fmt.Errorf("remove %s: %w", plistPath, err), bootoutErr)
	}

	return nil
}

// status reports launchd's view of the daemon.
func status(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "launchctl", "print", serviceTarget).CombinedOutput()
	if err != nil {
		if _, statErr := os.Stat(plistPath); os.IsNotExist(statErr) {
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
	args := daemonArgs(opts)

	var xmlArgs strings.Builder
	for _, a := range args {
		xmlArgs.WriteString("        <string>" + a + "</string>\n")
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
</dict>
</plist>
`, xmlArgs.String())

	return plistPath, content, nil
}

// runQuiet runs a prepared command, folding its output into any error.
func runQuiet(cmd *exec.Cmd) error {
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", strings.Join(cmd.Args, " "), err, strings.TrimSpace(string(out)))
	}

	return nil
}
