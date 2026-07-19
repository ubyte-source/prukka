//go:build !darwin && !linux && !windows

package native

import (
	"os"
	"os/exec"
	"testing"
)

func TestDirectProcessTreeAttachment(t *testing.T) {
	process := &os.Process{Pid: os.Getpid()}
	cmd := &exec.Cmd{Process: process}
	prepareProcessTree(cmd)

	tree, err := attachProcessTree(cmd)
	if err != nil {
		t.Fatalf("attachProcessTree: %v", err)
	}
	direct, ok := tree.(*directProcessTree)
	if !ok || direct.process != process {
		t.Fatalf("attached tree = %#v, want direct wrapper for current process", tree)
	}
	if err := tree.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
