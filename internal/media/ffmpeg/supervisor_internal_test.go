package ffmpeg

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// referenceFormat is the s16le mono pipe output every demux ends with.
func referenceFormat() []string {
	return []string{flagFormat, "s16le", "-ar", "16000", "-ac", "1", pipeOut, "-vn"}
}

// assertContainsAll fails if any token is missing from args.
func assertContainsAll(t *testing.T, args, want []string) {
	t.Helper()

	for _, w := range want {
		if !slices.Contains(args, w) {
			t.Errorf("args missing %q: %v", w, args)
		}
	}
}

// assertContainsNone fails if any substring appears in the joined args.
func assertContainsNone(t *testing.T, args, miss []string) {
	t.Helper()

	joined := strings.Join(args, " ")
	for _, m := range miss {
		if strings.Contains(joined, m) {
			t.Errorf("args unexpectedly contain %q: %v", m, args)
		}
	}
}

func TestPCMArgsPerScheme(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		src      string
		wantHas  []string
		wantMiss []string
	}{
		{
			name:    "rtmp listens",
			src:     "rtmp://0.0.0.0:1935/in/demo",
			wantHas: []string{"-listen", "1", "rtmp://0.0.0.0:1935/in/demo"},
		},
		{
			name:    "srt gets listener mode",
			src:     "srt://0.0.0.0:8890",
			wantHas: []string{"srt://0.0.0.0:8890?mode=listener"},
		},
		{
			name:     "srt with query keeps it",
			src:      "srt://0.0.0.0:8890?latency=200",
			wantHas:  []string{"srt://0.0.0.0:8890?latency=200&mode=listener"},
			wantMiss: []string{"?mode=listener"},
		},
		{
			name:     "srt with explicit mode untouched",
			src:      "srt://host?mode=caller",
			wantHas:  []string{"srt://host?mode=caller"},
			wantMiss: []string{"mode=listener"},
		},
		{
			name:     "file is pulled at real time",
			src:      "file:///tmp/x.wav",
			wantHas:  []string{"-re", "-i", "file:///tmp/x.wav"},
			wantMiss: []string{"-listen"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			args := pcmArgs(tc.src, "", 0)
			assertContainsAll(t, args, tc.wantHas)
			assertContainsAll(t, args, referenceFormat())
			assertContainsNone(t, args, tc.wantMiss)
			assertContainsNone(t, args, []string{"hls"})
		})
	}
}

func TestPCMArgsVideoTap(t *testing.T) {
	t.Parallel()

	args := pcmArgs("rtmp://0.0.0.0:1935/in/x", "/media/x/video", 8*time.Second)

	// The single process both pipes PCM and copies video into a rolling
	// HLS rendition; the optional map keeps audio-only sources working.
	assertContainsAll(t, args, []string{
		pipeOut,
		"-map", "0:v:0?", "-c:v", "copy", "hls",
		"/media/x/video/index.m3u8",
	})

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "delete_segments") {
		t.Fatalf("video rendition must roll its window: %v", args)
	}

	// PCM must be mapped explicitly or the second -map would steal it.
	if !strings.Contains(joined, "-map 0:a:0 ") {
		t.Fatalf("audio map missing: %v", args)
	}

	// The session delay shifts the rendition clock.
	if !strings.Contains(joined, "-output_ts_offset 8.000") {
		t.Fatalf("video rendition missing the delay shift: %v", args)
	}
}

func TestS16LEDescribesTheFormat(t *testing.T) {
	t.Parallel()

	got := s16le()
	want := []string{"-f", "s16le", "-ar", "16000", "-ac", "1"}

	if !slices.Equal(got, want) {
		t.Fatalf("s16le = %v, want %v", got, want)
	}
}

func TestDeviceTimelineRepairIsAVFoundationScoped(t *testing.T) {
	t.Parallel()

	want := []string{"-af", "aresample=16000:async=1:min_hard_comp=0.001:first_pts=0"}
	for _, src := range []string{"device://audio/1", "device://av/0|1"} {
		if got := deviceTimelineArgs("darwin", src); !slices.Equal(got, want) {
			t.Fatalf("deviceTimelineArgs(darwin, %q) = %v, want %v", src, got, want)
		}
	}
	for _, tc := range []struct{ goos, src string }{
		{goos: "windows", src: "device://audio/Microphone"},
		{goos: "linux", src: "device://audio/default"},
		{goos: "darwin", src: "rtmp://localhost/live"},
	} {
		if got := deviceTimelineArgs(tc.goos, tc.src); got != nil {
			t.Fatalf("deviceTimelineArgs(%q, %q) = %v, want nil", tc.goos, tc.src, got)
		}
	}
}

func TestArgvConcatenates(t *testing.T) {
	t.Parallel()

	got := argv([]string{"a", "b"}, []string{"c"}, nil, []string{"d"})
	if !slices.Equal(got, []string{"a", "b", "c", "d"}) {
		t.Fatalf("argv = %v", got)
	}
}

// TestPCMArgsEncodesAVSources: a paired camera capture demuxes PCM to the
// pipe and, with a video tap, encodes the raw camera frames (nothing to
// "copy") into the HLS rendition.
func TestPCMArgsEncodesAVSources(t *testing.T) {
	t.Parallel()

	joined := strings.Join(pcmArgs("device://av/0|1", "/tmp/video", 0), " ")

	if !strings.Contains(joined, "libx264") || strings.Contains(joined, "-c:v copy") {
		t.Fatalf("args = %q, want encoded video for a raw camera", joined)
	}

	if !strings.Contains(joined, "pipe:1") && !strings.Contains(joined, " - ") {
		t.Fatalf("args = %q, want the PCM pipe output", joined)
	}
}
