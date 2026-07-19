//go:build windows

package procio

import (
	"math"
	"os"
	"os/exec"
	"slices"
	"testing"
)

func TestPrepareTreeLeavesTheCommandUntouched(t *testing.T) {
	t.Parallel()

	cmd := &exec.Cmd{}
	PrepareTree(cmd)
	if cmd.SysProcAttr != nil {
		t.Fatalf("SysProcAttr = %#v, want none: the job object is armed after Start", cmd.SysProcAttr)
	}
}

func TestCheckedWindowsPIDRejectsOutOfRangeIdentifiers(t *testing.T) {
	t.Parallel()

	if _, err := checkedWindowsPID(-1); err == nil {
		t.Fatal("negative process id accepted")
	}
	if math.MaxInt > math.MaxUint32 {
		if _, err := checkedWindowsPID(math.MaxUint32 + 1); err == nil {
			t.Fatal("process id beyond uint32 accepted")
		}
	}
	if got, err := checkedWindowsPID(os.Getpid()); err != nil || int(got) != os.Getpid() {
		t.Fatalf("checkedWindowsPID(self) = (%d, %v), want the running pid", got, err)
	}
}

// TestWindowsTreeReleaseNeverWalksTheProcessTable is the contract test for the
// post-reap half of procio.Tree. terminateLocked matches live processes on a
// recorded ParentProcessID, a number Windows reuses aggressively and never
// clears from the entries of a dead parent's children — so once cmd.Wait has
// released the child's PID that walk can terminate a stranger. Release must
// therefore free the job handle only, and leave a later Kill inert. The fixture
// carries no PID and no job, so nothing real is ever signaled.
func TestWindowsTreeReleaseNeverWalksTheProcessTable(t *testing.T) {
	t.Parallel()

	released := &windowsTree{}
	if err := released.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := released.Kill(); err != nil {
		t.Fatalf("kill after release: %v", err)
	}
	if released.terminated {
		t.Fatal("Kill after Release walked the process table, want a no-op")
	}

	live := &windowsTree{}
	if err := live.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if !live.terminated {
		t.Fatal("Kill on an unreleased tree did not terminate: the fixture proves nothing")
	}
}

// TestSnapshotWindowsChildrenLinksThisProcessToItsParent reads the process
// relation the terminate walk depends on. It only reads: terminating real
// PIDs from a unit test would kill the runner.
func TestSnapshotWindowsChildrenLinksThisProcessToItsParent(t *testing.T) {
	t.Parallel()

	children, snapshotErr := snapshotWindowsChildren()
	if snapshotErr != nil {
		t.Fatalf("snapshotWindowsChildren: %v", snapshotErr)
	}

	self, err := checkedWindowsPID(os.Getpid())
	if err != nil {
		t.Fatalf("checkedWindowsPID(self): %v", err)
	}
	found := false
	for _, siblings := range children {
		if slices.Contains(siblings, self) {
			found = true

			break
		}
	}
	if !found {
		t.Fatalf("snapshot of %d parents never lists this process (%d) as a child", len(children), self)
	}
}

// TestTerminateWindowsDescendantsIgnoresAnEmptyRelation pins the guards that
// keep the walk from touching a PID when there is nothing to sweep.
func TestTerminateWindowsDescendantsIgnoresAnEmptyRelation(t *testing.T) {
	t.Parallel()

	self, err := checkedWindowsPID(os.Getpid())
	if err != nil {
		t.Fatalf("checkedWindowsPID(self): %v", err)
	}
	if err := terminateWindowsDescendants(nil, self); err != nil {
		t.Fatalf("empty relation = %v, want nil", err)
	}
	if err := terminateWindowsDescendants(map[uint32][]uint32{1: {2}}, 0); err != nil {
		t.Fatalf("unknown root = %v, want nil", err)
	}
}
