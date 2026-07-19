//go:build !windows

package devices

import (
	"os"
	"strings"
	"testing"
)

func TestPrivilegeCommandsNameTheRunningBinary(t *testing.T) {
	t.Parallel()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if hint := InstallHint(); !strings.Contains(hint, exe) || !strings.HasPrefix(hint, "sudo ") {
		t.Fatalf("InstallHint = %q, want the privileged path to this binary", hint)
	}

	err = RequirePrivilege("remove")
	if os.Geteuid() == 0 {
		if err != nil {
			t.Fatalf("RequirePrivilege as root = %v, want nil", err)
		}

		return
	}
	if err == nil || !strings.Contains(err.Error(), exe) || !strings.Contains(err.Error(), "devices remove") {
		t.Fatalf("RequirePrivilege = %v, want the exact sudo command", err)
	}
}
