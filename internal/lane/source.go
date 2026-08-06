package lane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
	"github.com/ubyte-source/prukka/internal/media/ingest/file"
	"github.com/ubyte-source/prukka/internal/media/ingest/stream"
	"github.com/ubyte-source/prukka/internal/paths"
	"github.com/ubyte-source/prukka/internal/redact"
	"github.com/ubyte-source/prukka/internal/speech"
)

// MicCaptureHelper resolves the managed native audio-device helper; the
// playback wiring and the ffmpeg supervisor must agree on one path.
func MicCaptureHelper() string {
	return ffmpeg.MicCaptureBinary(speech.BundleRoot(paths.StateDir()))
}

// ingressFor picks the adapter by URL scheme: WAV files native, everything
// else through the supervised ffmpeg splitter (resolved lazily).
func ingressFor(url string, profile session.Profile, log *slog.Logger) (core.Ingress, error) {
	scheme, rest, _ := strings.Cut(url, "://")

	switch scheme {
	case "file":
		path, _, _ := strings.Cut(rest, "?")
		if strings.HasSuffix(strings.ToLower(path), ".wav") {
			options := []file.Option(nil)
			if profile == session.ProfileCall {
				options = append(options, file.WithPCMQuantum(callMediaQuantum))
			}

			return file.New(options...), nil
		}

		fallthrough
	case "rtmp", "srt", "device":
		bin, err := ffmpeg.Resolve(paths.StateDir())
		if err != nil {
			return nil, err
		}

		supervisorOptions := []ffmpeg.SupervisorOption(nil)
		if helper := MicCaptureHelper(); helper != "" {
			supervisorOptions = append(supervisorOptions, ffmpeg.WithMicCapture(helper))
		}

		options := []stream.Option(nil)
		if profile == session.ProfileCall {
			options = append(options,
				stream.WithPCMQuantum(callMediaQuantum),
				stream.WithDeviceCaptureBuffer(callMediaQuantum),
			)
		}

		return stream.New(ffmpeg.NewSupervisor(bin, log, supervisorOptions...), options...), nil
	default:
		return nil, fmt.Errorf(
			"source %s: supported schemes are file, rtmp, srt and device", redact.URL(url))
	}
}

// lazyFrames defers opening a source until the lane first asks for media. Close
// may race the initial Open: if an ingress returns a source after cancellation,
// the opener closes that result instead of publishing or leaking it.
type lazyFrames struct {
	ingress core.Ingress
	frames  core.Frames
	source  *core.SourceSpec

	// closeSource is the source's single close owner, installed under mu the
	// instant open resolves and before open decides whether to publish or
	// abandon. It is nil for exactly as long as there is no source to close.
	closeSource func() error
	openErr     error
	openOnce    sync.Once
	mu          sync.Mutex
	closed      bool
}

func newLazyFrames(ingress core.Ingress, source core.SourceSpec) *lazyFrames {
	return &lazyFrames{ingress: ingress, source: &source}
}

func (f *lazyFrames) Next(ctx context.Context) (core.PCM, error) {
	if err := ctx.Err(); err != nil {
		return core.PCM{}, err
	}

	f.openOnce.Do(func() { f.open(ctx) })
	if err := ctx.Err(); err != nil {
		return core.PCM{}, err
	}

	f.mu.Lock()
	frames, openErr, closed := f.frames, f.openErr, f.closed
	f.mu.Unlock()

	if openErr != nil {
		return core.PCM{}, openErr
	}
	if closed {
		return core.PCM{}, io.ErrClosedPipe
	}
	if frames == nil {
		return core.PCM{}, errors.New("lazy ingress returned no frames")
	}

	return frames.Next(ctx)
}

func (f *lazyFrames) open(ctx context.Context) {
	f.mu.Lock()
	if f.closed {
		f.openErr = io.ErrClosedPipe
		f.mu.Unlock()

		return
	}
	f.mu.Unlock()

	frames, openErr := f.ingress.Open(ctx, *f.source)
	if frames == nil && openErr == nil {
		openErr = errors.New("ingress returned no frames")
	}
	ctxErr := ctx.Err()

	f.mu.Lock()
	if frames != nil {
		f.closeSource = sync.OnceValue(frames.Close)
	}
	closed := f.closed
	if openErr == nil && ctxErr == nil && !closed {
		f.frames = frames
		f.mu.Unlock()

		return
	}
	if closed {
		openErr = errors.Join(openErr, io.ErrClosedPipe)
	}
	f.openErr = errors.Join(openErr, ctxErr)
	abandon := f.closeSource
	f.mu.Unlock()

	if abandon == nil {
		return
	}

	closeErr := abandon()

	f.mu.Lock()
	f.openErr = errors.Join(f.openErr, closeErr)
	f.mu.Unlock()
}

// Close prevents a future open and interrupts an already-open source. It does
// not wait for an Open still in progress: that goroutine owns closing whatever
// it receives, and this call then reports no source result.
func (f *lazyFrames) Close() error {
	f.mu.Lock()
	f.closed = true
	closeSource := f.closeSource
	f.mu.Unlock()

	if closeSource == nil {
		return nil
	}

	return closeSource()
}

// observedFrames proves a lane is running only after media actually flows.
type observedFrames struct {
	core.Frames

	running func()
	once    sync.Once
}

func (f *observedFrames) Next(ctx context.Context) (core.PCM, error) {
	frame, err := f.Frames.Next(ctx)
	if err == nil {
		f.once.Do(f.running)
	}

	return frame, err
}
