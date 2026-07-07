package audio_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
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

func TestServeTSStartFailureReleasesFinitePlayout(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.DiscardHandler)
	sup := ffmpeg.NewSupervisor(filepath.Join(t.TempDir(), "missing-ffmpeg"), log)
	r := audio.NewRegistry(t.Context(), sup, nil, log)
	bed := pipeline.NewTrack()
	voice := pipeline.NewTrack()
	bed.Finish()
	voice.Finish()
	r.Create("finite", "en", pipeline.NewMixer(bed, voice, -15))

	if !r.ServeTS(t.Context(), io.Discard, "finite", "en") {
		t.Fatal("ServeTS reported an available pair as unknown")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := r.WaitPlayout(ctx, "finite"); err != nil {
		t.Fatalf("failed ServeTS left a cursor registered: %v", err)
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

func TestSetSupervisorActivatesFutureJobs(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.DiscardHandler)
	r := audio.NewRegistry(t.Context(), nil, nil, log)
	r.Create("late-setup", "en", mixer())

	before := r.Push("late-setup", "en", "rtmp://127.0.0.1/live", "off")
	if !errors.Is(before, core.ErrNotReady) {
		t.Fatalf("Push before SetSupervisor = %v, want ErrNotReady", before)
	}

	r.SetSupervisor(ffmpeg.NewSupervisor(filepath.Join(t.TempDir(), "missing-ffmpeg"), log))
	after := r.Push("late-setup", "en", "rtmp://127.0.0.1/live", "off")
	if after == nil || errors.Is(after, core.ErrNotReady) {
		t.Fatalf("Push after SetSupervisor = %v, want an attempted process start", after)
	}
}

func TestNativeVideoPushRejectsUnsupportedShapes(t *testing.T) {
	t.Parallel()

	r := audio.NewRegistry(context.Background(), nil, nil, slog.New(slog.DiscardHandler))
	if err := r.Push("demo", "de", ffmpeg.NativeVideoTarget, "burn"); err == nil {
		t.Fatal("native webcam accepted burned subtitles")
	}
	if err := r.Push("demo", "de", ffmpeg.NativeVideoTarget, "off"); err == nil {
		t.Fatal("native webcam accepted a session without video")
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

func TestWaitPlayoutFinalizesFiniteHLS(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.DiscardHandler)
	r := audio.NewRegistry(t.Context(), ffmpeg.NewSupervisor(testBinary(t), log), nil, log)
	bed := pipeline.NewTrack()
	bed.Append(0, make([]int16, 3*1600))
	bed.Finish()
	voice := pipeline.NewTrack()
	tail := make([]int16, 1600)
	for i := range tail {
		tail[i] = 9000
	}
	voice.Append(300*time.Millisecond, tail)
	voice.Finish()
	r.Create("finite-hls", "en", pipeline.NewMixer(bed, voice, -15))
	t.Cleanup(func() { r.Drop("finite-hls") })

	dir := t.TempDir()
	if err := r.StartHLS("finite-hls", "en", dir, 0); err != nil {
		t.Fatalf("StartHLS: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := r.WaitPlayout(ctx, "finite-hls"); err != nil {
		t.Fatalf("WaitPlayout: %v", err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open HLS output root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close HLS output root: %v", closeErr)
		}
	})
	playlist, err := root.ReadFile("index.m3u8")
	if err != nil {
		t.Fatalf("read finalized HLS playlist: %v", err)
	}
	if !strings.Contains(string(playlist), "#EXT-X-ENDLIST") {
		t.Fatalf("playlist was not finalized before WaitPlayout returned:\n%s", playlist)
	}
	segments, err := filepath.Glob(filepath.Join(dir, "seg*.ts"))
	if err != nil || len(segments) == 0 {
		t.Fatalf("finalized HLS segments = %v, err %v", segments, err)
	}
}

// TestPushAllowsMultipleTargets (live): audio and video routes for the same
// pair must be able to coexist instead of canceling one another.
func TestPushAllowsMultipleTargets(t *testing.T) {
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
		t.Fatalf("second-target Push returned error: %v", err)
	}

	r.Drop("push")
}
