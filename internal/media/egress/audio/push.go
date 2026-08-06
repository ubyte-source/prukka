package audio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/hostos"
	"github.com/ubyte-source/prukka/internal/media/deviceurl"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
	"github.com/ubyte-source/prukka/internal/media/wasapi"
	"github.com/ubyte-source/prukka/internal/redact"
)

// pushOrigin is where one route start came from: re-recording a replay would
// resurrect an intent a concurrent Drop deleted.
type pushOrigin uint8

const (
	pushRequested pushOrigin = iota
	pushReplayed
)

// Push streams one language's output to an RTMP or device:// target; the job
// outlives the RPC and re-pushing a target replaces only that target's job.
func (r *Registry) Push(slug string, lang core.Lang, target string, subs session.SubsMode) error {
	id := jobID{kind: kindPush, pair: pairID{slug: slug, lang: lang}, target: target}

	return r.push(id, subs, pushRequested)
}

// push runs one route start, recording a pushRequested not-ready intent.
func (r *Registry) push(id jobID, subs session.SubsMode, origin pushOrigin) error {
	teardowns := r.live.teardownCount()

	err := r.dispatchPush(id, subs)
	if origin == pushReplayed || (err != nil && !errors.Is(err, core.ErrNotReady)) {
		return err
	}
	if limitErr := r.rememberPush(id, subs, err == nil, teardowns); limitErr != nil {
		return limitErr
	}

	return err
}

func (r *Registry) dispatchPush(id jobID, subs session.SubsMode) error {
	target := id.target
	if deviceurl.Is(target) {
		return r.pushDevice(id, subs)
	}
	format, err := networkMux(target)
	if err != nil {
		return err
	}

	audioArgs := append(aacArgs(), ffmpeg.OutputArgs(format, target)...)

	playlist, hasVideo := r.video.VideoPlaylist(id.pair.slug)
	if !hasVideo {
		return r.startJob(id, audioArgs)
	}

	video := append([]string{"-c:v", "libx264", "-preset", "veryfast", "-b:v", "2500k"}, aacArgs()...)
	video = append(video, ffmpeg.OutputArgs(format, target)...)

	return r.startAVJob(id, playlist, r.burnFilter(id.pair, subs), video)
}

func networkMux(target string) (string, error) {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" {
		return "", errors.New("push target is not a valid network URL")
	}

	switch strings.ToLower(parsed.Scheme) {
	case "rtmp", "rtmps":
		return "flv", nil
	case "srt":
		return "mpegts", nil
	default:
		return "", fmt.Errorf("push target scheme %q: supported schemes are rtmp, rtmps, srt and device", parsed.Scheme)
	}
}

// burnFilter builds the subs=burn overlay filter, or "" when impossible —
// a logged downgrade, never silent.
func (r *Registry) burnFilter(pair pairID, subs session.SubsMode) string {
	if subs != session.SubsBurn {
		return ""
	}

	cueFile, ok := r.video.CueFile(pair.slug, string(pair.lang))
	if !ok {
		r.log.Warn("burn-in unavailable: no live cue overlay", "session", pair.slug, "lang", pair.lang)

		return ""
	}

	font := ffmpeg.DefaultFontFile()
	if font == "" {
		r.log.Warn("burn-in unavailable: no system font found", "session", pair.slug, "lang", pair.lang)

		return ""
	}

	return ffmpeg.BurnFilter(cueFile, font)
}

// pushDevice routes a push into a local device: audio takes the dub mix,
// video needs the session's video rendition.
func (r *Registry) pushDevice(id jobID, subs session.SubsMode) error {
	target := id.target
	if deviceurl.IsNativeVideo(target) {
		if subs == session.SubsBurn {
			return fmt.Errorf("%s does not support burned subtitles yet", redact.URL(target))
		}

		playlist, hasVideo := r.video.VideoPlaylist(id.pair.slug)
		if !hasVideo {
			return fmt.Errorf("%w: device target needs the session video rendition", core.ErrNotReady)
		}

		return r.startVideoDeviceJob(id, playlist)
	}

	pacing := r.live.pacingFor(id.pair)
	bufferDuration := deviceBufferDuration(pacing)

	ref, err := deviceurl.Parse(target)
	if err != nil {
		return deviceRefusal(target, err)
	}
	if ref.Kind == deviceurl.Audio {
		if runtime.GOOS == hostos.Windows {
			return r.launchMixFedJob(id, func(context.Context) (io.WriteCloser, error) {
				return wasapi.Open(target, wasapi.WithBufferDuration(bufferDuration))
			})
		}

		return r.startDeviceAudioJob(id, ref)
	}

	out, err := ffmpeg.DeviceOutputArgs(target, r.outputIndexResolver)
	if err != nil {
		return deviceRefusal(target, err)
	}

	playlist, hasVideo := r.video.VideoPlaylist(id.pair.slug)
	if !hasVideo {
		return fmt.Errorf("%w: device target needs the session video rendition", core.ErrNotReady)
	}

	return r.startAVJob(id, playlist, r.deviceVideoChain(id, subs), append([]string{"-an"}, out...))
}

// deviceRefusal renders a lower layer's refusal for the caller, replacing the
// exact target with redact.URL: sweeping every URL-shaped token instead would
// collapse a reduction a lower layer already made.
func deviceRefusal(target string, err error) error {
	return errors.New(strings.ReplaceAll(err.Error(), target, redact.URL(target)))
}

// deviceVideoChain composes the -vf chain for a device video push: geometry
// before captions, both in ONE -vf, because a second -vf silently replaces
// the first.
func (r *Registry) deviceVideoChain(id jobID, subs session.SubsMode) string {
	vf := r.burnFilter(id.pair, subs)
	deviceVF := ffmpeg.DeviceVideoFilter(id.target)
	switch {
	case deviceVF == "":
		return vf
	case vf == "":
		return deviceVF
	default:
		return deviceVF + "," + vf
	}
}

// deviceBufferDuration keeps two feed quanta queued in the platform playback
// layer: 40 ms for a call's 20 ms quantum, 200 ms for the 100 ms broadcast.
func deviceBufferDuration(pacing feedConfig) time.Duration {
	return 2 * pacing.quantum
}

// startVideoDeviceJob feeds the session's video rendition straight into a
// native video device, which reads the playlist itself and rides no pair mix.
func (r *Registry) startVideoDeviceJob(id jobID, playlist string) error {
	return r.launchSelfFedJob(id, func(
		ctx context.Context, sup *ffmpeg.Supervisor,
	) (<-chan error, error) {
		return sup.StartVideoDevice(ctx, playlist, id.target)
	})
}

// startDeviceAudioJob launches the audio-device push: the native helper binds
// the output device by NAME, immune to the array reshuffling Continuity
// devices cause, while the ffmpeg fallback rebuilds its arguments on every
// (re)open so a reopen rebinds the label to the device's current index.
func (r *Registry) startDeviceAudioJob(id jobID, ref deviceurl.Ref) error {
	if ref.Label != "" {
		if helper := r.resolvePlaybackHelper(); helper != "" {
			return r.launchMixFedJob(id, func(ctx context.Context) (io.WriteCloser, error) {
				return ffmpeg.StartDevicePlayback(ctx, helper, ref.Label, core.SampleRate, r.log)
			})
		}
	}

	sup, err := r.requireSupervisor(id.pair)
	if err != nil {
		return err
	}
	// Reject a malformed target at push time, not at first reopen.
	if _, err := ffmpeg.DeviceOutputArgs(id.target, r.outputIndexResolver); err != nil {
		return deviceRefusal(id.target, err)
	}

	return r.launchMixFedJob(id, deviceAudioSinkStarter(sup, id.target, r.outputIndexResolver))
}

func (r *Registry) resolvePlaybackHelper() string {
	if r.playbackHelper == nil {
		return ""
	}

	return r.playbackHelper()
}

// sinkStarter starts one encoder process over prepared arguments; the ffmpeg
// supervisor satisfies it, and tests substitute a recorder.
type sinkStarter interface {
	StartSink(ctx context.Context, args []string) (io.WriteCloser, error)
}

// deviceAudioSinkStarter returns the start hook for an audio-device push.
func deviceAudioSinkStarter(
	sup sinkStarter, target string, resolve ffmpeg.OutputIndexResolver,
) func(context.Context) (io.WriteCloser, error) {
	return func(ctx context.Context) (io.WriteCloser, error) {
		args, err := ffmpeg.DeviceOutputArgs(target, resolve)
		if err != nil {
			return nil, err
		}

		return sup.StartSink(ctx, args)
	}
}
