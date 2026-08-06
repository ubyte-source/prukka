package procio_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/procio"
	"github.com/ubyte-source/prukka/internal/redact"
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

// TestWithStderrDeclaresTheChildsOwnText pins the boundary contract: the tail
// stays readable to whoever holds the error and is named as foreign, so a
// renderer bound by redact.Untrusted drops that exact span.
func TestWithStderrDeclaresTheChildsOwnText(t *testing.T) {
	t.Parallel()

	cause := errors.New("exit status 1")
	tail := "whisper_init: failed to load /Users/alice/Secret Model/model.bin"

	if err := procio.WithStderr(cause, ""); !errors.Is(err, cause) || err.Error() != cause.Error() {
		t.Fatalf("an empty tail must leave the cause alone, got %v", err)
	}

	err := procio.WithStderr(cause, tail)
	if !errors.Is(err, cause) {
		t.Fatalf("WithStderr broke the chain: %v", err)
	}
	if !strings.Contains(err.Error(), tail) {
		t.Fatalf("Error dropped the tail a caller needs: %v", err)
	}

	var foreign redact.Untrusted
	if !errors.As(err, &foreign) {
		t.Fatalf("WithStderr did not declare its text foreign: %v", err)
	}
	if foreign.Untrusted() != tail {
		t.Fatalf("declared span = %q, want the tail verbatim", foreign.Untrusted())
	}
}
