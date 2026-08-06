//go:build !bundleddrivers

package devices

import (
	"errors"
	"testing"
)

func TestPayloadsReportNotBundled(t *testing.T) {
	t.Parallel()

	if _, err := payloads(); !errors.Is(err, ErrNotBundled) {
		t.Fatalf("payloads = %v, want ErrNotBundled", err)
	}
}
