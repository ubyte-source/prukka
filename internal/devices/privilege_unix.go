//go:build !windows

package devices

import (
	"fmt"
	"os"
)

// InstallHint is the privileged command that (re)installs the drivers on
// this OS, spelled with the running binary's own path — PATH rarely
// carries it right after install and sudo resets PATH regardless.
func InstallHint() string {
	return "sudo " + executable() + " devices install"
}

// RequirePrivilege fails fast when the verb needs root, naming the exact
// command to run, before any driver file is touched.
func RequirePrivilege(verb string) error {
	if os.Geteuid() == 0 {
		return nil
	}

	return fmt.Errorf("managing drivers needs root — run: sudo %s devices %s", executable(), verb)
}

// privilegeHint completes permission errors with the missing privilege.
const privilegeHint = "root required — try sudo"
