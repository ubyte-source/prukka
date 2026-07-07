//go:build windows

package wasapi

import (
	"strings"
	"testing"
)

// TestOpenRejectsMalformedTargets: target validation happens before any
// COM call, so a bad URL fails fast with the expected shape named.
func TestOpenRejectsMalformedTargets(t *testing.T) {
	t.Parallel()

	for _, target := range []string{"", "bogus", "device://video/0", "device://audio/"} {
		w, err := Open(target)
		if err == nil || w != nil {
			t.Fatalf("Open(%q) = (%v, %v), want a validation error", target, w, err)
		}

		if !strings.Contains(err.Error(), "device://audio/") {
			t.Fatalf("Open(%q) error %q does not name the expected shape", target, err)
		}
	}
}
