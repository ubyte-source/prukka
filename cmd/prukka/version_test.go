package main

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

// TestVersionPrintsBuildInfo: the one command support asks for first must
// name the product, the commit and the exact toolchain.
func TestVersionPrintsBuildInfo(t *testing.T) {
	t.Parallel()

	cmd := newVersionCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("version returned error: %v", err)
	}

	for _, want := range []string{"prukka ", "commit:", runtime.Version(), runtime.GOOS} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("version output %q lacks %q", out.String(), want)
		}
	}
}
