package audio

// The push layer: what a push target IS (a network URL or a local device),
// which encoder arguments and start hook it needs, and what one Push call
// means. It decides and delegates — every job start here crosses into
// audio.go's lifecycle layer, and no function in this file takes a lock.

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
	"github.com/ubyte-source/prukka/internal/hostos"
	"github.com/ubyte-source/prukka/internal/media/ffmpeg"
	"github.com/ubyte-source/prukka/internal/media/wasapi"
)

// Push streams one language's output to an RTMP or device:// target. Jobs
// outlive the RPC, and re-pushing a target replaces only that target's job.
// Healthy and merely-not-ready outcomes are remembered as session intents so
// a rebuilt lane relaunches them; a target the daemon can never serve is not.
func (r *Registry) Push(session, lang, target, subs string) error {
	return r.push(session, lang, target, subs, true)
}

// push runs one route start; remember records not-ready intents. The route
// REPLAY path passes remember=false: replaying an intent must not resurrect
// it after a concurrent Drop already deleted the session — the intent either
// still exists (Drop not run) or must stay dead.
func (r *Registry) push(session, lang, target, subs string, remember bool) error {
	// Read the teardown count BEFORE dispatching: everything the dispatch
	// learns about the session it learns after this point, so a Drop that lands
	// later invalidates the intent this call would otherwise leave behind.
	teardowns := r.teardowns.Load()

	err := r.dispatchPush(session, lang, target, subs)
	if !remember || (err != nil && !errors.Is(err, core.ErrNotReady)) {
		return err
	}
	// Refusing a not-ready intent here is honest: nothing is streaming, so the
	// caller can be told the pair is full instead of being told "pushed" about
	// a target the next lane restart would silently forget.
	route := pushRoute{session: session, lang: lang, target: target, subs: subs}
	if limitErr := r.rememberPush(route, err == nil, teardowns); limitErr != nil {
		return limitErr
	}

	return err
}

func (r *Registry) dispatchPush(session, lang, target, subs string) error {
	if ffmpeg.IsDeviceURL(target) {
		return r.pushDevice(session, lang, target, subs)
	}
	format, err := networkMux(target)
	if err != nil {
		return err
	}

	audioArgs := append(append([]string{}, aacArgs...), ffmpeg.OutputArgs(format, target)...)

	playlist, hasVideo := r.video.VideoPlaylist(session)
	if !hasVideo {
		return r.startJob("push", session, lang, target, audioArgs)
	}

	video := make([]string, 0, 8+len(aacArgs))
	video = append(video, "-c:v", "libx264", "-preset", "veryfast", "-b:v", "2500k")
	video = append(video, aacArgs...)
	video = append(video, ffmpeg.OutputArgs(format, target)...)

	return r.startAVJob("push", session, lang, target, playlist, r.burnFilter(session, lang, subs), video)
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
func (r *Registry) burnFilter(session, lang, subs string) string {
	if subs != "burn" {
		return ""
	}

	cueFile, ok := r.video.CueFile(session, lang)
	if !ok {
		r.log.Warn("burn-in unavailable: no live cue overlay", "session", session, "lang", lang)

		return ""
	}

	font := ffmpeg.DefaultFontFile()
	if font == "" {
		r.log.Warn("burn-in unavailable: no system font found", "session", session, "lang", lang)

		return ""
	}

	return ffmpeg.BurnFilter(cueFile, font)
}

// pushDevice routes a push into a local device: audio takes the dub mix,
// video needs the session's video rendition.
func (r *Registry) pushDevice(session, lang, target, subs string) error {
	if ffmpeg.IsNativeVideoTarget(target) {
		if subs == "burn" {
			return fmt.Errorf("device target %q does not support burned subtitles yet", target)
		}

		playlist, hasVideo := r.video.VideoPlaylist(session)
		if !hasVideo {
			return fmt.Errorf("%w: device target needs the session video rendition", core.ErrNotReady)
		}

		return r.startVideoDeviceJob(session, lang, playlist, target)
	}

	pacing := r.pacingFor(session, lang)
	bufferDuration := deviceBufferDuration(pacing)
	if ffmpeg.IsAudioDeviceTarget(target) && runtime.GOOS == hostos.Windows {
		return r.launchMixFedJob("push", session, lang, target, func(context.Context) (io.WriteCloser, error) {
			return wasapi.Open(target, wasapi.WithBufferDuration(bufferDuration))
		})
	}

	if ffmpeg.IsAudioDeviceTarget(target) {
		return r.startDeviceAudioJob(session, lang, target)
	}

	out, err := ffmpeg.DeviceOutputArgs(target, r.outputIndexResolver)
	if err != nil {
		return err
	}

	playlist, hasVideo := r.video.VideoPlaylist(session)
	if !hasVideo {
		return fmt.Errorf("%w: device target needs the session video rendition", core.ErrNotReady)
	}

	return r.startAVJob("push", session, lang, target, playlist,
		r.deviceVideoChain(session, lang, subs, target), append([]string{"-an"}, out...))
}

// deviceVideoChain composes the -vf chain for a device video push: a
// fixed-mode device dictates geometry BEFORE captions burn, so the overlay
// renders at the node's real resolution. Both stages ride ONE -vf —
// StartAVSink passes a single filter and a second -vf would silently
// replace the first.
func (r *Registry) deviceVideoChain(session, lang, subs, target string) string {
	vf := r.burnFilter(session, lang, subs)
	deviceVF := ffmpeg.DeviceVideoFilter(target)
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
// layer. Calls therefore request 40 ms while the 100 ms broadcast feed retains
// the existing 200 ms WASAPI behavior.
func deviceBufferDuration(pacing feedConfig) time.Duration {
	return 2 * pacing.quantum
}

// startVideoDeviceJob feeds the session's video rendition straight into a
// native video device: the process reads the playlist itself, so it rides no
// pair mix.
func (r *Registry) startVideoDeviceJob(session, lang, playlist, target string) error {
	return r.launchSelfFedJob("push", session, lang, target, func(
		ctx context.Context, sup *ffmpeg.Supervisor,
	) (<-chan error, error) {
		return sup.StartVideoDevice(ctx, playlist, target)
	})
}

// startDeviceAudioJob launches the audio-device push. A labeled target with
// the native helper available renders through the helper, which binds the
// output device by NAME — immune to the array reshuffling that Continuity
// devices cause. Otherwise the ffmpeg path applies, with its arguments
// rebuilt by the start hook on every (re)open so a reopen rebinds the label
// to the device's current index rather than injecting into whatever now sits
// at the stale position.
func (r *Registry) startDeviceAudioJob(session, lang, target string) error {
	if label := ffmpeg.DeviceTargetLabel(target); label != "" {
		if helper := r.resolvePlaybackHelper(); helper != "" {
			return r.launchMixFedJob("push", session, lang, target,
				func(ctx context.Context) (io.WriteCloser, error) {
					return ffmpeg.StartDevicePlayback(ctx, helper, label, core.SampleRate, r.log)
				})
		}
	}

	sup, err := r.requireSupervisor(session, lang)
	if err != nil {
		return err
	}
	// Reject a malformed target at push time, not at first reopen.
	if _, err := ffmpeg.DeviceOutputArgs(target, r.outputIndexResolver); err != nil {
		return err
	}

	return r.launchMixFedJob("push", session, lang, target,
		deviceAudioSinkStarter(sup, target, r.outputIndexResolver))
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

// deviceAudioSinkStarter returns the start hook for an audio-device push,
// resolving the device arguments fresh on every call.
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
