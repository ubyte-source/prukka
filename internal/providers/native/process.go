package native

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"
)

const helperStopGrace = 500 * time.Millisecond

type processTree interface {
	kill() error
	close() error
}

// closeTreeAndWait retires every PID-based tree operation while the child PID
// is still reserved, then reaps the child. A process-tree implementation may
// keep an identity-bearing kernel handle (for example, a Windows Job Object),
// but it must make kill a no-op before close returns.
func closeTreeAndWait(tree processTree, wait func() error) (waitErr, treeErr error) {
	treeErr = tree.close()
	waitErr = wait()

	return waitErr, treeErr
}

// warmProcess owns the common lifetime of a cached MT or TTS helper. Its
// protocol-specific read pump is the sole caller of finish and therefore Wait.
type warmProcess struct {
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	exitErr    error
	cleanupErr error
	tree       processTree
	cmd        *exec.Cmd
	stderr     *tailBuffer
	log        *slog.Logger
	stop       chan struct{}
	done       chan struct{}
	stage      string

	abortOnce sync.Once
}

// startWarmProcess wires and starts cmd. The caller must immediately start a
// read pump that eventually calls finish, including after abort closes stdout.
func startWarmProcess(
	ctx context.Context, cmd *exec.Cmd, stage string, log *slog.Logger,
) (*warmProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	stderr := &tailBuffer{limit: stderrTail}
	cmd.Stderr = stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("%s stdin: %w", stage, err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("%s stdout: %w", stage, err), stdin.Close())
	}

	// Own cancellation so stdin gets a graceful close before the tree kill.
	cmd.Cancel = nil
	prepareProcessTree(cmd)
	if err := cmd.Start(); err != nil {
		return nil, errors.Join(fmt.Errorf("start %s: %w", stage, err), stdin.Close(), stdout.Close())
	}

	tree, treeErr := attachProcessTree(cmd)
	proc := &warmProcess{
		stdin: stdin, stdout: stdout, stage: stage, cmd: cmd, stderr: stderr,
		tree: tree, log: log, stop: make(chan struct{}), done: make(chan struct{}),
	}
	if treeErr != nil {
		log.Warn("native helper process-tree fallback", "stage", stage, "err", treeErr)
	}

	return proc, nil
}

// watch aborts the helper with its owning context. The read pump then observes
// the closed stdout pipe and reaps the process.
func (p *warmProcess) watch(ctx context.Context) {
	select {
	case <-ctx.Done():
		p.abort()
	case <-p.done:
	}
}

// abort closes input for an orderly shutdown, then kills the whole process tree
// if the helper does not exit within the grace period.
func (p *warmProcess) abort() {
	p.abortOnce.Do(func() {
		close(p.stop)
		if err := p.stdin.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			p.cleanupErr = fmt.Errorf("close %s input: %w", p.stage, err)
			p.log.Debug("native helper close input", "stage", p.stage, "err", err)
		}

		go p.forceStop()
	})
}

// close stops the process tree, waits for reap and reports cleanup failures.
func (p *warmProcess) close() error {
	p.abort()
	<-p.done

	return p.cleanupErr
}

func (p *warmProcess) forceStop() {
	timer := time.NewTimer(helperStopGrace)
	defer timer.Stop()

	select {
	case <-p.done:
		return
	case <-timer.C:
	}

	if err := p.tree.kill(); err != nil {
		p.log.Debug("native helper kill tree", "stage", p.stage, "err", err)
	}
	if err := p.stdout.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		p.log.Debug("native helper close output", "stage", p.stage, "err", err)
	}
}

// finish retires the process tree, reaps the child and publishes one terminal
// protocol error. It must be called only by the protocol read pump, after
// scanning has ended.
func (p *warmProcess) finish(stage string, scanErr error) {
	// The read pump owns Wait. Once stdout ends there is no valid response the
	// process can still produce, so close its input and arm the bounded tree
	// kill before waiting. This also prevents an oversized scanner token from
	// deadlocking while the helper remains alive waiting for another request.
	p.abort()
	waitErr, treeErr := closeTreeAndWait(p.tree, p.cmd.Wait)
	p.cleanupErr = errors.Join(p.cleanupErr, treeErr)
	cause := errors.Join(scanErr, waitErr, treeErr)
	if cause == nil {
		cause = io.ErrUnexpectedEOF
	}

	p.exitErr = withHelperStderr(fmt.Errorf("%s exited: %w", stage, cause), p.stderr)

	close(p.done)
}

// writeLine writes one JSON line and lets cancellation interrupt a blocked
// pipe write. Aborting is required because a partial request cannot be reused.
func (p *warmProcess) writeLine(ctx context.Context, line []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	stopAbort := context.AfterFunc(ctx, p.abort)
	n, err := p.stdin.Write(line)
	stopAbort()

	if ctxErr := ctx.Err(); ctxErr != nil {
		// The complete request may already be in the pipe. Stop this helper so
		// its unread response cannot be mistaken for the next request's reply.
		p.abort()

		return ctxErr
	}
	if err != nil {
		return p.writeFailure(ctx, err)
	}
	if n != len(line) {
		return p.writeFailure(ctx, io.ErrShortWrite)
	}

	return nil
}

// writeFailure makes a partial request terminal, waits for the bounded abort
// to reap the helper, then returns both the pipe cause and its final diagnostic.
// Context cancellation remains authoritative when it races the failed write.
func (p *warmProcess) writeFailure(ctx context.Context, writeErr error) error {
	p.abort()
	<-p.done
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}

	return errors.Join(writeErr, p.exitErr)
}

func withHelperStderr(err error, stderr *tailBuffer) error {
	if stderr == nil {
		return err
	}

	return withHelperStderrTail(err, stderr.String())
}

func withHelperStderrTail(err error, tail string) error {
	if tail != "" {
		return fmt.Errorf("%w; stderr: %s", err, tail)
	}

	return err
}

// unusable reports whether the helper is stopping or has already exited.
func (p *warmProcess) unusable() bool {
	select {
	case <-p.stop:
		return true
	case <-p.done:
		return true
	default:
		return false
	}
}

// finished reports whether Wait has returned and exitErr is available.
func (p *warmProcess) finished() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// stopping reports whether abort, rather than a natural exit, ended the pipe.
func (p *warmProcess) stopping() bool {
	select {
	case <-p.stop:
		return true
	default:
		return false
	}
}

// terminalError waits until Wait has published the process failure.
func (p *warmProcess) terminalError() error {
	<-p.done

	return p.exitErr
}

// acquire serializes one request/turn while still honoring cancellation.
func acquire(ctx context.Context, gate, done <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return io.ErrUnexpectedEOF
	case <-gate:
		return nil
	}
}
