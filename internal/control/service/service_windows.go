//go:build windows

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ubyte-source/prukka/internal/core/config"

	"github.com/ubyte-source/prukka/internal/procio"
)

// The daemon runs as the logged-in user so its state directory, control pipe
// and interactive media devices keep the same ownership and visibility. A
// per-user logon task provides that boundary without elevation.
//
// taskName identifies that task in the scheduler.
const taskName = "Prukka"

// endSettleAttempts bounds the wait for an ended instance to disappear
// before /Run; the daemon's control pipe vanishing is the signal.
const endSettleAttempts = 20

// endSettleDelay paces that wait; a variable so tests do not sleep.
var endSettleDelay = 250 * time.Millisecond

// install registers (or replaces) the logon task from its XML definition.
func install(ctx context.Context, opts *Options) error {
	_, content, err := rendered(opts)
	if err != nil {
		return err
	}

	tmp, stageErr := stagedDefinition(content)
	if stageErr != nil {
		return stageErr
	}

	createErr := procio.RunQuiet(exec.CommandContext(ctx, "schtasks", "/Create", "/TN", taskName, "/XML", tmp, "/F"))
	if joined := errors.Join(createErr, os.Remove(tmp)); joined != nil {
		return joined
	}

	if !opts.Now {
		return nil
	}

	return endThenRun(ctx)
}

// endThenRun stops any running instance and starts a fresh one, waiting
// for the old daemon's control pipe to vanish in between: with
// MultipleInstancesPolicy IgnoreNew, a /Run issued while the scheduler
// still counts the ending instance is silently dropped.
func endThenRun(ctx context.Context) error {
	// Ending fails when the task is not running; that error only matters
	// if the fresh start also fails.
	endErr := procio.RunQuiet(exec.CommandContext(ctx, "schtasks", "/End", "/TN", taskName))

	for range endSettleAttempts {
		if _, statErr := os.Stat(config.IPCPath()); errors.Is(statErr, os.ErrNotExist) {
			break
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(endSettleDelay):
		}
	}

	if runErr := procio.RunQuiet(exec.CommandContext(ctx, "schtasks", "/Run", "/TN", taskName)); runErr != nil {
		return errors.Join(runErr, endErr)
	}

	return nil
}

// stagedDefinition writes the task XML to a temporary file for schtasks
// to read and returns its path.
func stagedDefinition(content string) (string, error) {
	tmp, err := os.CreateTemp("", "prukka-task-*.xml")
	if err != nil {
		return "", fmt.Errorf("stage task definition: %w", err)
	}

	if writeErr := errors.Join(writeAll(tmp, content), tmp.Close()); writeErr != nil {
		return "", fmt.Errorf("write task definition: %w", errors.Join(writeErr, os.Remove(tmp.Name())))
	}

	return tmp.Name(), nil
}

// restart relaunches the daemon task.
func restart(ctx context.Context) error {
	return endThenRun(ctx)
}

// remove ends the daemon and deletes the task; removing an uninstalled
// service succeeds.
func remove(ctx context.Context) error {
	if !installed(ctx) {
		return nil
	}

	// Ending fails when the task is not running; that alone is fine. It is
	// only surfaced if the deletion also fails.
	endErr := procio.RunQuiet(exec.CommandContext(ctx, "schtasks", "/End", "/TN", taskName))

	deleteCmd := exec.CommandContext(ctx, "schtasks", "/Delete", "/TN", taskName, "/F")
	if deleteErr := procio.RunQuiet(deleteCmd); deleteErr != nil {
		return errors.Join(deleteErr, endErr)
	}

	return nil
}

// status reports the task's registration and, since schtasks localizes
// its state column, reads liveness from the daemon's control pipe.
func status(ctx context.Context) (string, error) {
	if !installed(ctx) {
		return "not installed", nil
	}

	if _, statErr := os.Stat(config.IPCPath()); statErr != nil && errors.Is(statErr, os.ErrNotExist) {
		return "installed (not running)", nil
	}

	return "running", nil
}

// installed reports whether the logon task is registered.
func installed(ctx context.Context) bool {
	return procio.RunQuiet(exec.CommandContext(ctx, "schtasks", "/Query", "/TN", taskName)) == nil
}

// rendered produces the task-scheduler XML definition.
func rendered(opts *Options) (path, content string, err error) {
	me, userErr := user.Current()
	if userErr != nil {
		return "", "", fmt.Errorf("resolve current user: %w", userErr)
	}

	// A scheduled task captures no output; without --log-file every daemon
	// line would vanish and field problems could not be diagnosed.
	args := append(daemonArgs(opts), "--log-file", filepath.Join(config.StateDir(), "daemon.log"))

	content = fmt.Sprintf(`<?xml version="1.0"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Real-time multilingual dubbing and interpretation engine.</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>%[1]s</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>%[1]s</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>3</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%[2]s</Command>
      <Arguments>%[3]s</Arguments>
    </Exec>
  </Actions>
</Task>
`, xmlEscape(me.Username), xmlEscape(args[0]), xmlEscape(argumentsLine(args[1:])))

	return fmt.Sprintf("(scheduled task %q)", taskName), content, nil
}

// argumentsLine renders the daemon arguments as one command-line string,
// quoting the ones that need it.
func argumentsLine(args []string) string {
	var line strings.Builder
	for i, arg := range args {
		if i > 0 {
			line.WriteString(" ")
		}

		line.WriteString(syscall.EscapeArg(arg))
	}

	return line.String()
}

// xmlEscaper covers the five XML special characters.
var xmlEscaper = strings.NewReplacer(
	"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;",
)

// xmlEscape makes a value safe inside the task definition.
func xmlEscape(value string) string {
	return xmlEscaper.Replace(value)
}

// writeAll writes the whole payload to the staged file.
func writeAll(f *os.File, content string) error {
	_, err := f.WriteString(content)

	return err
}
