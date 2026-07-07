package ffmpeg

import (
	"testing"
)

func TestTailBufferKeepsTheTail(t *testing.T) {
	t.Parallel()

	tb := &tailBuffer{limit: 8}
	for _, s := range []string{"hello ", "world ", "goodbye"} {
		if _, err := tb.Write([]byte(s)); err != nil {
			t.Fatalf("Write returned error: %v", err)
		}
	}

	// Only the last 8 bytes are retained (trimmed of surrounding space).
	if got := tb.String(); got != "goodbye" {
		t.Fatalf("tail = %q, want the last bytes", got)
	}
}
