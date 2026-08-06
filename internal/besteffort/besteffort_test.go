package besteffort_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ubyte-source/prukka/internal/besteffort"
)

// failingWriter fails every write.
type failingWriter struct{ calls int }

func (w *failingWriter) Write([]byte) (int, error) {
	w.calls++

	return 0, errors.New("sink closed")
}

func TestIgnoreSwallowsAFailureAndANilAlike(t *testing.T) {
	t.Parallel()

	besteffort.Ignore(errors.New("boom"))
	besteffort.Ignore(nil)
}

func TestLinefFormatsItsArgumentsAndAppendsTheNewline(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	besteffort.Linef(&out, "installing %s: %d%%", "voice-it", 40)

	if got, want := out.String(), "installing voice-it: 40%\n"; got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}
}

func TestLinefNarratesNothingToANilWriter(t *testing.T) {
	t.Parallel()

	besteffort.Linef(nil, "dropped %d", 1)
}

func TestLinefSurvivesAWriterThatFailsEveryLine(t *testing.T) {
	t.Parallel()

	sink := &failingWriter{}
	besteffort.Linef(sink, "first")
	besteffort.Linef(sink, "second")

	if sink.calls != 2 {
		t.Fatalf("writes attempted = %d, want 2: a failed line must not stop the next one", sink.calls)
	}
}
