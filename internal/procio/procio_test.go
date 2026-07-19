package procio_test

import (
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/procio"
)

func TestTailBufferKeepsOnlyTheNewestBytes(t *testing.T) {
	t.Parallel()

	tail := procio.NewTailBuffer(8)
	for _, chunk := range []string{"one ", "two ", "three"} {
		if n, err := tail.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = (%d, %v)", chunk, n, err)
		}
	}

	if got := tail.String(); got != "wo three" {
		t.Fatalf("tail = %q, want the last 8 bytes trimmed", got)
	}
}

// A single write larger than the limit must keep its newest bytes rather
// than growing past the bound; a non-positive limit retains nothing.
func TestTailBufferHandlesOversizedWritesAndZeroLimit(t *testing.T) {
	t.Parallel()

	tail := procio.NewTailBuffer(4)
	if _, err := tail.Write([]byte("abcdefgh")); err != nil {
		t.Fatal(err)
	}
	if got := tail.String(); got != "efgh" {
		t.Fatalf("oversized write tail = %q, want %q", got, "efgh")
	}

	disabled := procio.NewTailBuffer(0)
	if _, err := disabled.Write([]byte("noise")); err != nil {
		t.Fatal(err)
	}
	if got := disabled.String(); got != "" {
		t.Fatalf("disabled tail = %q, want empty", got)
	}
}

func TestTailBufferTrimsSurroundingWhitespace(t *testing.T) {
	t.Parallel()

	tail := procio.NewTailBuffer(64)
	if _, err := tail.Write([]byte("  helper failed: model missing\n")); err != nil {
		t.Fatal(err)
	}
	if got := tail.String(); strings.ContainsAny(got[:1]+got[len(got)-1:], " \n") {
		t.Fatalf("tail %q keeps surrounding whitespace", got)
	}
}
