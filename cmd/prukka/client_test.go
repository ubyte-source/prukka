package main

import (
	"bytes"
	"errors"
	"testing"
)

// failWriter fails every write.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("pipe closed") }

// TestRowJoinsColumnsWithTabs: table output must stay tab-separated so
// tabwriter aligns it and scripts can cut it.
func TestRowJoinsColumnsWithTabs(t *testing.T) {
	t.Parallel()

	out := &bytes.Buffer{}
	if err := row(out, "a", "b", "c"); err != nil {
		t.Fatalf("row returned error: %v", err)
	}

	if out.String() != "a\tb\tc\n" {
		t.Fatalf("row wrote %q", out.String())
	}
}

func TestRowSurfacesWriteFailures(t *testing.T) {
	t.Parallel()

	if err := row(failWriter{}, "a"); err == nil {
		t.Fatal("a failed write went unreported")
	}
}
