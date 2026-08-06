package ffmpeg

import (
	"slices"
	"testing"
)

func TestOutputArgsFlushMPEGTSOnly(t *testing.T) {
	t.Parallel()

	ts := OutputArgs("mpegts", "srt://example.test:9000")
	for _, token := range []string{"-muxdelay", "-muxpreload", "-flush_packets"} {
		if !slices.Contains(ts, token) {
			t.Fatalf("MPEG-TS args %v missing %s", ts, token)
		}
	}
	flv := OutputArgs("flv", "rtmp://example.test/live/key")
	if slices.Contains(flv, "-muxdelay") || !slices.Equal(
		flv, []string{"-f", "flv", "rtmp://example.test/live/key"},
	) {
		t.Fatalf("FLV args = %v", flv)
	}
}
