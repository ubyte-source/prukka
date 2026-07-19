//go:build !windows

package deploy_test

import (
	"os"
	"testing"
)

// readUninstaller returns the committed uninstall.sh the install tests
// re-read to drive teardown.
func readUninstaller(t *testing.T) []byte {
	t.Helper()

	body, err := os.ReadFile("uninstall.sh")
	if err != nil {
		t.Fatalf("read uninstaller: %v", err)
	}

	return body
}
