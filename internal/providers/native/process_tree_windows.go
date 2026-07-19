//go:build windows

package native

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsProcessTree struct {
	process      *os.Process
	terminateErr error
	closeErr     error
	job          windows.Handle
	mu           sync.Mutex
	pid          uint32
	terminated   bool
	closed       bool
}

func prepareProcessTree(*exec.Cmd) {}

func attachProcessTree(cmd *exec.Cmd) (processTree, error) {
	tree := &windowsProcessTree{process: cmd.Process}
	pid, err := checkedWindowsPID(cmd.Process.Pid)
	if err != nil {
		return tree, err
	}
	tree.pid = pid

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return tree, fmt.Errorf("create job object: %w", err)
	}

	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		return tree, errors.Join(fmt.Errorf("configure job object: %w", err), windows.CloseHandle(job))
	}

	handle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		tree.pid,
	)
	if err != nil {
		return tree, errors.Join(fmt.Errorf("open process for job: %w", err), windows.CloseHandle(job))
	}

	assignErr := windows.AssignProcessToJobObject(job, handle)
	closeErr := windows.CloseHandle(handle)
	if assignErr != nil {
		return tree, errors.Join(
			fmt.Errorf("assign process to job: %w", assignErr),
			closeErr,
			windows.CloseHandle(job),
		)
	}

	tree.job = job
	if closeErr != nil {
		return tree, fmt.Errorf("close process handle after job assignment: %w", closeErr)
	}

	return tree, nil
}

func (t *windowsProcessTree) kill() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil
	}

	return t.terminateLocked()
}

func (t *windowsProcessTree) close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return t.closeErr
	}

	// Permanently disable kill before the caller reaps cmd. Any fallback
	// snapshot therefore observes the original, still-reserved parent PID.
	t.closed = true
	terminateErr := t.terminateLocked()
	var closeErr error
	if t.job != 0 {
		closeErr = windows.CloseHandle(t.job)
		t.job = 0
	}
	t.closeErr = errors.Join(terminateErr, closeErr)

	return t.closeErr
}

func (t *windowsProcessTree) terminateLocked() error {
	if t.terminated {
		return t.terminateErr
	}
	t.terminated = true

	// Snapshot the descendant relation before terminating anything: a grandchild
	// spawned in the window between the child's start and its job assignment
	// escaped the job, and once the helper dies the snapshot can no longer link
	// that grandchild back to the helper PID. This runs on the job path too, not
	// only the assignment-rejected fallback. It is intentionally never run after
	// Wait, so the PIDs are still reserved.
	children, snapshotErr := t.snapshotChildren()

	var rootErr error
	if t.job != 0 {
		// TerminateJobObject kills the child and every descendant enrolled in the
		// job; the walk below sweeps up any that started before assignment.
		rootErr = windows.TerminateJobObject(t.job, 1)
	} else {
		// Job assignment was rejected because the parent already belonged to a
		// job; terminate the identity-bearing os.Process handle directly.
		rootErr = killWindowsProcess(t.process)
	}

	descendantErr := terminateWindowsDescendants(children, t.pid)
	t.terminateErr = errors.Join(snapshotErr, rootErr, descendantErr)

	return t.terminateErr
}

func (t *windowsProcessTree) snapshotChildren() (map[uint32][]uint32, error) {
	if t.pid == 0 {
		return map[uint32][]uint32{}, nil
	}

	return snapshotWindowsChildren()
}

func checkedWindowsPID(pid int) (uint32, error) {
	value := int64(pid)
	if value < 0 || value > math.MaxUint32 {
		return 0, fmt.Errorf("process id %d is outside uint32", pid)
	}

	return uint32(value), nil
}

func snapshotWindowsChildren() (children map[uint32][]uint32, retErr error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("snapshot processes: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, windows.CloseHandle(snapshot)) }()

	children = make(map[uint32][]uint32)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	if err = windows.Process32First(snapshot, &entry); err != nil {
		return nil, fmt.Errorf("read process snapshot: %w", err)
	}
	for {
		children[entry.ParentProcessID] = append(children[entry.ParentProcessID], entry.ProcessID)
		if err = windows.Process32Next(snapshot, &entry); err != nil {
			break
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, fmt.Errorf("walk process snapshot: %w", err)
	}

	return children, nil
}

func terminateWindowsDescendants(children map[uint32][]uint32, pid uint32) error {
	if len(children) == 0 || pid == 0 {
		return nil
	}

	seen := map[uint32]bool{pid: true}
	var errs []error
	for _, child := range children[pid] {
		errs = append(errs, terminateWindowsBranch(children, child, seen))
	}

	return errors.Join(errs...)
}

func terminateWindowsBranch(children map[uint32][]uint32, pid uint32, seen map[uint32]bool) error {
	if seen[pid] {
		return nil
	}
	seen[pid] = true

	var errs []error
	for _, child := range children[pid] {
		errs = append(errs, terminateWindowsBranch(children, child, seen))
	}
	errs = append(errs, terminateWindowsProcess(pid))

	return errors.Join(errs...)
}

func terminateWindowsProcess(pid uint32) error {
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open process %d: %w", pid, err)
	}

	terminateErr := windows.TerminateProcess(handle, 1)
	if errors.Is(terminateErr, windows.ERROR_ACCESS_DENIED) {
		// Windows reports access denied when termination already completed.
		terminateErr = nil
	}
	closeErr := windows.CloseHandle(handle)

	return errors.Join(terminateErr, closeErr)
}

func killWindowsProcess(process *os.Process) error {
	if process == nil {
		return nil
	}

	err := process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}

	return err
}
