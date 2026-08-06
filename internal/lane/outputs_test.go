package lane

import (
	"context"
	"math"
	"testing"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/core/realtime"
	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/media/egress/audio"
	"github.com/ubyte-source/prukka/internal/media/egress/vtt"
)

// nopSynth is a synthesizer that yields no audio.
type nopSynth struct{}

func (nopSynth) Close() error { return nil }

func (nopSynth) Speak(
	context.Context, core.Lang, core.Voice, <-chan string,
) (*realtime.AudioStream, error) {
	out := make(chan core.PCM)
	close(out)
	result := make(chan error, 1)
	result <- nil
	close(result)

	return realtime.NewAudioStream(out, result), nil
}

// The runtime takes its two teardown hooks as one value, so a restart cannot be
// wired to the deletion path.
var _ session.LaneOutputs = Outputs{}

func TestCaptionSinksDiscardWhenSubtitlesAreOff(t *testing.T) {
	t.Parallel()

	registry := vtt.NewRegistry()
	s := &session.Session{
		Slug:  "dub-only",
		Langs: []core.Lang{"it"},
		Subs:  session.SubsOff,
	}
	sinks := captionSinks(s, &runDeps{out: Outputs{vtt: registry}}, nil)
	sinks["it"].Append(&core.TranslatedSegment{Text: "ciao"})

	if _, ok := registry.Document("dub-only", "it"); ok {
		t.Fatal("subs=off registered a direct WebVTT document")
	}
}

func TestCallProfileSkipsRollingCaptionMedia(t *testing.T) {
	t.Parallel()

	s := &session.Session{
		Slug:    "fast-call",
		Profile: session.ProfileCall,
		Langs:   []core.Lang{"en"},
		Source:  core.SourceSpec{URL: "device://audio/microphone"},
	}
	if media := createCaptionMedia(s, &runDeps{}); media != nil {
		t.Fatalf("call media = %v, want no HLS tree", media)
	}
	av := &session.Session{
		Profile: session.ProfileCall,
		Source:  core.SourceSpec{URL: "device://av/camera"},
	}
	if skipCaptionMedia(av) {
		t.Fatal("AV call skipped the video rendition needed by device pushes")
	}
}

func TestBuildDub(t *testing.T) {
	t.Parallel()

	s := &session.Session{Slug: "dub", Langs: []core.Lang{"it"}}
	registry := audio.NewRegistry(t.Context(), nil, nil, discard())

	if dub := buildDub(s, &runDeps{out: Outputs{audio: registry}}, nil); dub != nil {
		t.Fatalf("captions-only dub = %v, want none", dub)
	}

	deps := &runDeps{synth: nopSynth{}, out: Outputs{audio: registry}, voices: []core.Voice{{ID: "v"}}}
	dub := buildDub(s, deps, nil)
	if dub == nil || dub.Tracks["it"] == nil || dub.Voices["it"].ID != "v" || dub.Bed == nil {
		t.Fatalf("dub = %v, want a bed, a track and a voice per language", dub)
	}

	mixed := &session.Session{Slug: "mixed", Langs: []core.Lang{"it", "en-GB"}}
	englishOnly := &runDeps{
		synth:  nopSynth{},
		out:    Outputs{audio: registry},
		voices: []core.Voice{{ID: "en", Lang: "en"}},
		log:    discard(),
	}
	dub = buildDub(mixed, englishOnly, nil)
	if dub == nil || dub.Tracks["en-GB"] == nil || dub.Tracks["it"] != nil {
		t.Fatalf("language-scoped dub tracks = %v, want only en-GB", dub)
	}
}

// TestBuildDubUsesBedlessQueueForCallsOnly: broadcast mixes each voice over a
// shared delayed bed; a call has neither bed nor mixer.
func TestBuildDubUsesBedlessQueueForCallsOnly(t *testing.T) {
	t.Parallel()

	registry := audio.NewRegistry(t.Context(), nil, nil, discard())
	deps := func() *runDeps {
		return &runDeps{
			synth:  nopSynth{},
			out:    Outputs{audio: registry},
			voices: []core.Voice{{ID: "en", Lang: "en"}},
			log:    discard(),
		}
	}

	broadcast := &session.Session{Slug: "bcast", Profile: session.ProfileBroadcast, Langs: []core.Lang{"en"}}
	dub := buildDub(broadcast, deps(), nil)
	if dub == nil || dub.Bed == nil {
		t.Fatalf("broadcast dub = %+v, want a shared bed", dub)
	}
	if _, ok := dub.Tracks["en"].(*pipeline.Track); !ok {
		t.Fatalf("broadcast target = %T, want a bed-mixed *pipeline.Track", dub.Tracks["en"])
	}

	call := &session.Session{Slug: "call", Profile: session.ProfileCall, Langs: []core.Lang{"en"}}
	dub = buildDub(call, deps(), nil)
	if dub == nil || dub.Bed != nil {
		t.Fatalf("call dub = %+v, want no bed", dub)
	}
	if _, ok := dub.Tracks["en"].(*pipeline.VoiceQueue); !ok {
		t.Fatalf("call target = %T, want *pipeline.VoiceQueue", dub.Tracks["en"])
	}
}

func TestBuildDubBindsOneVoicePerTarget(t *testing.T) {
	t.Parallel()

	registry := audio.NewRegistry(t.Context(), nil, nil, discard())
	s := &session.Session{Slug: "twoway", Langs: []core.Lang{"it", "en-GB"}}
	deps := &runDeps{
		synth:  nopSynth{},
		out:    Outputs{audio: registry},
		log:    discard(),
		voices: []core.Voice{{ID: "lessac", Lang: "en"}, {ID: "paola", Lang: "it"}},
	}

	dub := buildDub(s, deps, nil)
	if dub == nil || len(dub.Tracks) != 2 || len(dub.Voices) != 2 {
		t.Fatalf("dub = %+v, want two voiced targets", dub)
	}
	if dub.Voices["it"].ID != "paola" || dub.Voices["en-GB"].ID != "lessac" {
		t.Fatalf("per-target voices = %+v, want paola for it and lessac for en-GB", dub.Voices)
	}
}

func TestBedLevelOrFallsBackToTheConfigSnapshot(t *testing.T) {
	t.Parallel()

	cases := []struct {
		flag string
		want float64
	}{
		{flag: "-15dB", want: -15},
		{flag: "-9dB", want: -9},
		{flag: "-20", want: -20},
		{flag: "  -12dB ", want: -12},
		{flag: "", want: -9},
		{flag: "off", want: math.Inf(-1)},
	}

	for _, tc := range cases {
		if got := bedLevelOr(tc.flag, -9); got != tc.want {
			t.Errorf("bedLevelOr(%q) = %v, want %v", tc.flag, got, tc.want)
		}
	}
}
