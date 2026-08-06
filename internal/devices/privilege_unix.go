//go:build !windows

package devices

import (
	"fmt"
	"os"
)

// InstallHint is the privileged command that (re)installs the drivers on this
// OS.
func InstallHint() string {
	return "sudo " + executable() + " devices install"
}

// RequirePrivilege fails before any driver file is touched when verb needs root.
func RequirePrivilege(verb string) error {
	if os.Geteuid() == 0 {
		return nil
	}

	return fmt.Errorf("managing drivers needs root — run: sudo %s devices %s", executable(), verb)
}

// privilegeHint completes permission errors with the missing privilege.
const privilegeHint = "root required — try sudo"
