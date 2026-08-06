// Package besteffort spells out the work whose failure a caller has decided
// not to act on: the discard of an error, and the progress line an optional
// writer may drop.
package besteffort

import (
	"fmt"
	"io"
)

// Ignore discards the error of an operation whose failure the caller cannot
// act on. errcheck runs with check-blank, so `_ = f()` is unavailable and the
// discard has to consume the error through a call.
func Ignore(error) {}

// Linef writes one line of progress narration, appending the newline; a nil
// writer narrates nothing and a failing writer is not retried.
func Linef(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}

	_, err := fmt.Fprintf(w, format+"\n", args...)
	Ignore(err)
}
