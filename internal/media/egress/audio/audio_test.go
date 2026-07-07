package audio_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/media/egress/audio"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
)

// mixer builds a mixer over two empty tracks.
func mixer() *pipeline.Mixer {
	return pipeline.NewMixer(pipeline.NewTrack(), pipeline.NewTrack(), -15)
}

// testBinary locates an ffmpeg for live streaming tests: PRUKKA_TEST_FFMPEG
// wins, then PATH; skip without one.
func testBinary(t *testing.T) string {
	t.Helper()

	if bin := os.Getenv("PRUKKA_TEST_FFMPEG"); bin != "" {
		return bin
	}

	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg available; set PRUKKA_TEST_FFMPEG or install one")
	}

	return bin
}

func TestServeTSUnknownPairIsFalse(t *testing.T) {
	t.Parallel()

	// No ffmpeg supervisor and no registration: streaming is unavailable.
	r := audio.NewRegistry(context.Background(), nil, nil, slog.New(slog.DiscardHandler))

	if r.ServeTS(context.Background(), nil, "ghost", "en") {
		t.Fatal("ServeTS reported available for an unknown session")
	}
}

func TestServeTSWithoutSupervisorIsFalse(t *testing.T) {
	t.Parallel()

	// The pair exists but there is no ffmpeg to encode it.
	r := audio.NewRegistry(context.Background(), nil, nil, slog.New(slog.DiscardHandler))
	r.Create("demo", "en", mixer())

	if r.ServeTS(context.Background(), nil, "demo", "en") {
		t.Fatal("ServeTS reported available without an ffmpeg supervisor")
	}
}

func TestPushRequiresDubbedAudio(t *testing.T) {
	t.Parallel()

	r := audio.NewRegistry(context.Background(), nil, nil, slog.New(slog.DiscardHandler))

	if err := r.Push("ghost", "en", "rtmp://x/live", "off"); err == nil {
		t.Fatal("Push succeeded for an unknown pair, want error")
	}

	// Even a known pair cannot push without a supervisor.
	r.Create("demo", "en", mixer())
	if err := r.Push("demo", "en", "rtmp://x/live", "off"); err == nil {
		t.Fatal("Push succeeded without an ffmpeg supervisor, want error")
	}
}

// TestDropEndsLiveStream: a live stream must return once its session is
// dropped, not wait forever.
func TestDropEndsLiveStream(t *testing.T) {
	t.Parallel()

	sup := ffmpeg.NewSupervisor(testBinary(t), slog.New(slog.DiscardHandler))
	r := audio.NewRegistry(context.Background(), sup, nil, slog.New(slog.DiscardHandler))
	r.Create("live", "en", mixer())

	served := make(chan bool, 1)

	go func() { served <- r.ServeTS(context.Background(), io.Discard, "live", "en") }()

	// Let the encoder come up idle, then drop the session under it.
	time.Sleep(300 * time.Millisecond)
	r.Drop("live")

	select {
	case ok := <-served:
		if !ok {
			t.Fatal("ServeTS reported unavailable for a live pair")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeTS still running after Drop: the session gate did not end the stream")
	}
}

func TestDropRemovesEverySessionPair(t *testing.T) {
	t.Parallel()

	r := audio.NewRegistry(context.Background(), nil, nil, slog.New(slog.DiscardHandler))
	r.Create("keep", "en", mixer())
	r.Create("drop", "en", mixer())
	r.Create("drop", "de", mixer())

	r.Drop("drop")   // removes both drop/* pairs
	r.Drop("absent") // dropping an unknown session is safe

	// The dropped pairs no longer serve; the call must not panic either.
	if r.ServeTS(context.Background(), nil, "drop", "en") {
		t.Fatal("dropped pair still served")
	}
}

// TestStartHLSWritesARollingRendition (live): the real ffmpeg must produce
// the playlist and first segment on disk.
func TestStartHLSWritesARollingRendition(t *testing.T) {
	t.Parallel()

	sup := ffmpeg.NewSupervisor(testBinary(t), slog.New(slog.DiscardHandler))
	r := audio.NewRegistry(context.Background(), sup, nil, slog.New(slog.DiscardHandler))

	// The mixer anchors on the bed track: give it half a minute of audio so
	// the paced feed streams for the whole test.
	bed := pipeline.NewTrack()
	bed.Append(0, make([]int16, 30*16000))
	r.Create("hls", "en", pipeline.NewMixer(bed, pipeline.NewTrack(), -15))

	dir := t.TempDir()
	if err := r.StartHLS("hls", "en", dir, 0); err != nil {
		t.Fatalf("StartHLS returned error: %v", err)
	}

	defer r.Drop("hls")

	// The feed paces real time and segments are 4 s: the playlist and the
	// first segment must land well within the deadline.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(dir + "/index.m3u8"); err == nil {
			return
		}

		time.Sleep(250 * time.Millisecond)
	}

	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("no HLS playlist after 20s (and the dir is unreadable: %v)", readErr)
	}

	t.Fatalf("no HLS playlist after 20s; dir holds %d entries", len(entries))
}

// TestPushReplacesThePreviousJob (live): pushing the same pair twice must
// swap the encoder, not leak the first — the replace branch of launch.
func TestPushReplacesThePreviousJob(t *testing.T) {
	t.Parallel()

	sup := ffmpeg.NewSupervisor(testBinary(t), slog.New(slog.DiscardHandler))
	r := audio.NewRegistry(context.Background(), sup, nil, slog.New(slog.DiscardHandler))
	r.Create("push", "en", mixer())

	// An unroutable loopback target: the job starts; delivery failing later
	// is the job's business, never the caller's.
	if err := r.Push("push", "en", "rtmp://127.0.0.1:1/live/x", "off"); err != nil {
		t.Fatalf("first Push returned error: %v", err)
	}

	if err := r.Push("push", "en", "rtmp://127.0.0.1:1/live/y", "off"); err != nil {
		t.Fatalf("replacing Push returned error: %v", err)
	}

	r.Drop("push")
}
