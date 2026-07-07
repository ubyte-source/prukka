//go:build windows

package service

import (
	"strings"
	"testing"
)

// TestRenderedExplainsTheSCM: the preview says definitions live in the SCM
// and names the daemon invocation.
func TestRenderedExplainsTheSCM(t *testing.T) {
	t.Parallel()

	path, content, err := rendered(&Options{
		ExecPath:   `C:\prukka\prukka.exe`,
		ConfigPath: `C:\prukka\config.yaml`,
	})
	if err != nil {
		t.Fatalf("rendered returned error: %v", err)
	}

	if !strings.Contains(path, "service control manager") {
		t.Fatalf("path %q does not name the SCM", path)
	}

	for _, want := range []string{Name, `C:\prukka\prukka.exe`, "--config", "automatic"} {
		if !strings.Contains(content, want) {
			t.Fatalf("preview lacks %q:\n%s", want, content)
		}
	}
}
