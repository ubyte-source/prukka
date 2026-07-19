package native

import (
	"errors"

	"github.com/ubyte-source/prukka/internal/procio"
)

// killTreeAndReap retires a helper in the one order procio.Tree allows: Kill
// while the child PID is still reserved, then reap, then Release — the call
// that frees the handle and can no longer signal a number the kernel has
// handed on.
func killTreeAndReap(tree procio.Tree, wait func() error) (waitErr, treeErr error) {
	killErr := tree.Kill()
	waitErr = wait()

	return waitErr, errors.Join(killErr, tree.Release())
}
