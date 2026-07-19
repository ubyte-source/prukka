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

	"github.com/ubyte-source/prukka/internal/procio"
)

const helperStopGrace = 500 * time.Millisecond

// engineRootEnv mirrors internal/engine's PRUKKA_ENGINE_ROOT: the daemon sets it
// on a self-executed helper so the helper resolves the bundle it was pointed at,
// not its own (the daemon's) directory. It is a wire contract with the engine
// subcommands, matched on both ends like the stt|mt|tts verbs.
const engineRootEnv = "PRUKKA_ENGINE_ROOT"

// engineChildEnv is the environment for a spawned engine helper. A managed
// self-exec (root set) inherits the daemon's environment plus PRUKKA_ENGINE_ROOT;
// an operator-supplied binary (root empty) inherits it unchanged and resolves
// its own bundle, so nil keeps exec's default inheritance.
func engineChildEnv(root string) []string {
	if root == "" {
		return nil
	}

	return append(os.Environ(), engineRootEnv+"="+root)
}

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

// spawnedHelper is one wired stdio helper child: the piece of the lifecycle
// every owner shares, regardless of how it retires the process afterwards.
type spawnedHelper struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr *procio.TailBuffer
	tree   processTree
	cmd    *exec.Cmd
}

// spawnHelper wires and starts one helper child: stderr tail, both stdio
// pipes with joined cleanup on failure, caller-owned cancellation (stdin gets
// a graceful close before any tree kill) and the process tree attached, with
// a logged fallback when attachment fails.
func spawnHelper(
	ctx context.Context, cmd *exec.Cmd, stage string, log *slog.Logger,
) (*spawnedHelper, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	stderr := procio.NewTailBuffer(procio.DefaultStderrTail)
	cmd.Stderr = stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("%s stdin: %w", stage, err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("%s stdout: %w", stage, err), stdin.Close())
	}

	cmd.Cancel = nil
	prepareProcessTree(cmd)
	if err := cmd.Start(); err != nil {
		return nil, errors.Join(fmt.Errorf("start %s: %w", stage, err), stdin.Close(), stdout.Close())
	}

	tree, treeErr := attachProcessTree(cmd)
	if treeErr != nil {
		log.Warn("native helper process-tree fallback", "stage", stage, "err", treeErr)
	}

	return &spawnedHelper{stdin: stdin, stdout: stdout, stderr: stderr, tree: tree, cmd: cmd}, nil
}

// forceStop grants the helper helperStopGrace to exit on its own, then kills
// the tree and closes stdout so a blocked read pump observes the end.
func (h *spawnedHelper) forceStop(done <-chan struct{}, stage string, log *slog.Logger) {
	timer := time.NewTimer(helperStopGrace)
	defer timer.Stop()

	select {
	case <-done:
		return
	case <-timer.C:
	}

	if err := h.tree.kill(); err != nil {
		log.Debug("native helper kill tree", "stage", stage, "err", err)
	}
	if err := h.stdout.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		log.Debug("native helper close output", "stage", stage, "err", err)
	}
}

// warmProcess owns the common lifetime of a cached MT or TTS helper. Its
// protocol-specific read pump is the sole caller of finish and therefore Wait.
type warmProcess struct {
	*spawnedHelper

	exitErr    error
	cleanupErr error
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
	child, err := spawnHelper(ctx, cmd, stage, log)
	if err != nil {
		return nil, err
	}

	return &warmProcess{
		spawnedHelper: child, stage: stage, log: log,
		stop: make(chan struct{}), done: make(chan struct{}),
	}, nil
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
	p.spawnedHelper.forceStop(p.done, p.stage, p.log)
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

func withHelperStderr(err error, stderr *procio.TailBuffer) error {
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

// ErrClosed reports work submitted to a provider after Close; callers match
// it with errors.Is instead of prose.
var ErrClosed = errors.New("provider is closed")

// helperProc is the lifecycle a cached warm helper exposes to its cache.
type helperProc interface {
	comparable
	unusable() bool
	abort()
	close() error
}

// procCache owns one warm helper per key — MT keys by language pair, TTS by
// voice — with one choreography: get-or-spawn under the mutex, stale
// replacement reaped outside it, idempotent discard and a drain-abort-close
// shutdown.
type procCache[P helperProc] struct {
	closeErr  error
	life      context.Context
	cancel    context.CancelFunc
	log       *slog.Logger
	procs     map[string]P
	role      string
	mu        sync.Mutex
	closeOnce sync.Once
	closed    bool
}

// newProcCache wires an empty cache; role names the owner in errors and logs.
func newProcCache[P helperProc](role string, log *slog.Logger) *procCache[P] {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	cache := &procCache[P]{log: log, procs: map[string]P{}, role: role}
	cache.life, cache.cancel = cacheLifecycle()

	return cache
}

// cacheLifecycle exists to appease gosec G118, which cannot follow a
// CancelFunc stored through a generic struct literal (the cancel runs in
// closeAll). Callers must store BOTH results; dropping the CancelFunc here
// would also escape the analyzer.
func cacheLifecycle() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

// get returns the cached usable helper for key, spawning a replacement on
// first use or after the cached one became unusable. The stale helper is
// reaped outside the lock: its shutdown can take the full stop grace, and
// holding the lock through that would stall every other key.
func (c *procCache[P]) get(
	ctx context.Context, key string, spawn func(life context.Context, log *slog.Logger) (P, error),
) (P, error) {
	var zero P

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()

		return zero, fmt.Errorf("%s: %w", c.role, ErrClosed)
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()

		return zero, err
	}
	if proc, ok := c.procs[key]; ok && !proc.unusable() {
		c.mu.Unlock()

		return proc, nil
	}

	stale, hadStale := c.procs[key]
	proc, err := spawn(c.life, c.log)
	if err != nil {
		delete(c.procs, key)
		c.mu.Unlock()
		if hadStale {
			err = errors.Join(err, stale.close())
		}

		return zero, err
	}
	c.procs[key] = proc
	c.mu.Unlock()

	if hadStale {
		if cleanupErr := stale.close(); cleanupErr != nil {
			c.log.Warn("stale helper cleanup failed", "helper", c.role, "err", cleanupErr)
		}
	}

	return proc, nil
}

// discard removes proc only if it is still the cached helper for key, then
// reaps it outside the lock so its shutdown never stalls other keys.
func (c *procCache[P]) discard(key string, proc P) error {
	c.mu.Lock()
	if c.procs[key] == proc {
		delete(c.procs, key)
	}
	c.mu.Unlock()

	return proc.close()
}

// Close stops and reaps every cached helper. It is idempotent.
func (c *procCache[P]) Close() error {
	c.closeOnce.Do(func() { c.closeErr = c.closeAll() })

	return c.closeErr
}

func (c *procCache[P]) closeAll() error {
	c.mu.Lock()
	c.closed = true
	procs := make([]P, 0, len(c.procs))
	for key, proc := range c.procs {
		procs = append(procs, proc)
		delete(c.procs, key)
	}
	c.mu.Unlock()

	c.cancel()
	for _, proc := range procs {
		proc.abort()
	}
	cleanupErrs := make([]error, 0, len(procs))
	for _, proc := range procs {
		cleanupErrs = append(cleanupErrs, proc.close())
	}

	return errors.Join(cleanupErrs...)
}
