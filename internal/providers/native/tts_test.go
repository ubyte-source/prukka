package native

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
)

// speakOneResult synthesizes one clause and returns its PCM plus terminal result.
func speakOneResult(t *testing.T, tts *TTS, voice core.Voice, clause string) ([]int16, error) {
	t.Helper()

	text := make(chan string, 1)
	text <- clause
	close(text)

	audio, err := tts.Speak(t.Context(), core.Lang("en"), voice, text)
	if err != nil {
		t.Fatalf("speak: %v", err)
	}

	var got []int16
	for pcm := range audio.Audio() {
		got = append(got, pcm.Data...)
	}

	return got, audio.Err()
}

func speakOne(t *testing.T, tts *TTS, voice core.Voice, clause string) []int16 {
	t.Helper()

	got, err := speakOneResult(t, tts, voice, clause)
	if err != nil {
		t.Fatalf("synthesis stream: %v", err)
	}

	return got
}

func newTestTTS(t *testing.T, cfg *TTSConfig) *TTS {
	t.Helper()

	tts := NewTTS(cfg)
	t.Cleanup(func() {
		if err := tts.Close(); err != nil {
			t.Errorf("close test synthesizer: %v", err)
		}
	})

	return tts
}

func TestTTSSynthesizesClause(t *testing.T) {
	t.Parallel()

	tts := newTestTTS(t, &TTSConfig{Bin: os.Args[0], Rate: 16000})

	got := speakOne(t, tts, core.Voice{ID: fakeVoice}, "Buongiorno.")
	if !reflect.DeepEqual(got, fakeSamples) {
		t.Fatalf("pcm = %v, want %v", got, fakeSamples)
	}
}

func TestTTSSkipsBlankClauses(t *testing.T) {
	tts := newTestTTS(t, &TTSConfig{Bin: os.Args[0], Rate: 16000})
	voice := core.Voice{ID: fakeVoice}

	if got := speakOne(t, tts, voice, " \t\n"); len(got) != 0 {
		t.Fatalf("blank clause produced PCM %v", got)
	}
	if got := speakOne(t, tts, voice, "spoken"); !reflect.DeepEqual(got, fakeSamples) {
		t.Fatalf("PCM after blank clause = %v, want %v", got, fakeSamples)
	}
}

func TestTTSReusesWarmProcessPerVoice(t *testing.T) {
	t.Parallel()

	tts := newTestTTS(t, &TTSConfig{Bin: os.Args[0], Rate: 16000})

	speakOne(t, tts, core.Voice{ID: fakeVoice}, "uno")
	speakOne(t, tts, core.Voice{ID: fakeVoice}, "due")

	if got := procCount(tts); got != 1 {
		t.Fatalf("warm processes = %d, want 1 (voice reused)", got)
	}
}

func TestTTSWarmProcessOutlivesFirstCallContext(t *testing.T) {
	tts := newTestTTS(t, &TTSConfig{Bin: os.Args[0], Rate: 16000})
	voice := core.Voice{ID: fakeVoice}
	ctx, cancel := context.WithCancel(t.Context())
	text := make(chan string, 1)
	text <- "first"
	close(text)

	audio, err := tts.Speak(ctx, "en-US", voice, text)
	if err != nil {
		t.Fatalf("first synthesis: %v", err)
	}
	var first []int16
	for pcm := range audio.Audio() {
		first = append(first, pcm.Data...)
	}
	if !reflect.DeepEqual(first, fakeSamples) {
		t.Fatalf("first synthesis PCM = %v, want %v", first, fakeSamples)
	}
	if err = audio.Err(); err != nil {
		t.Fatalf("first synthesis stream: %v", err)
	}
	proc := voiceForTest(tts, voice.ID)
	cancel()

	select {
	case <-proc.stop:
		t.Fatal("finishing the first target stopped the provider-owned helper")
	case <-time.After(50 * time.Millisecond):
	}

	if got := speakOne(t, tts, voice, "second"); !reflect.DeepEqual(got, fakeSamples) {
		t.Fatalf("second synthesis PCM = %v, want %v", got, fakeSamples)
	}
	if current := voiceForTest(tts, voice.ID); current != proc {
		t.Fatal("regional targets did not share the provider-owned helper")
	}
}

func TestTTSCloseReapsHelpersAndRejectsNewWork(t *testing.T) {
	wantCleanup := errors.New("synthesis tree cleanup failed")
	tts := NewTTS(&TTSConfig{Bin: os.Args[0], Rate: 16000})
	t.Cleanup(func() {
		if err := tts.Close(); err != nil && !errors.Is(err, wantCleanup) {
			t.Errorf("close test synthesizer: %v", err)
		}
	})
	voice := core.Voice{ID: fakeVoice}
	speakOne(t, tts, voice, "warm")

	proc := voiceForTest(tts, voice.ID)
	if proc == nil {
		t.Fatal("synthesis helper was not cached")
	}
	proc.tree = &failingCloseTree{processTree: proc.tree, err: wantCleanup}

	closed := make(chan error, 2)
	go func() { closed <- tts.Close() }()
	go func() { closed <- tts.Close() }()
	for range 2 {
		if err := <-closed; !errors.Is(err, wantCleanup) {
			t.Fatalf("Close = %v, want cleanup failure", err)
		}
	}
	select {
	case <-proc.done:
	default:
		t.Fatal("Close returned before the synthesis helper was reaped")
	}

	_, err := tts.Speak(t.Context(), "en", voice, nil)
	if err == nil || !strings.Contains(err.Error(), "synthesizer is closed") {
		t.Fatalf("Speak after Close = %v, want closed error", err)
	}
}

func TestTTSDiscardReapsWithoutHoldingLock(t *testing.T) {
	warm, entered, release := blockedWarmProcess()
	defer release()
	proc := &voiceProc{warmProcess: warm}
	tts := newTestTTS(t, &TTSConfig{})
	tts.procs[fakeVoice] = proc

	discarded := make(chan error, 1)
	go func() { discarded <- tts.discard(fakeVoice, proc) }()
	<-entered

	// The helper is deleted from the map under the lock, but its shutdown runs
	// unlocked, so a concurrent Close and any other voice keep making progress.
	if !tts.mu.TryLock() {
		t.Fatal("discard held the provider lock across the process reap")
	}
	tts.mu.Unlock()

	closed := make(chan error, 1)
	go func() { closed <- tts.Close() }()
	if err := <-closed; err != nil {
		t.Fatalf("Close: %v", err)
	}

	release()
	if err := <-discarded; err != nil {
		t.Fatalf("discard: %v", err)
	}
}

func TestTTSCloseInterruptsActiveStream(t *testing.T) {
	tts := newTestTTS(t, &TTSConfig{Bin: os.Args[0], Rate: 16000})
	voice := core.Voice{ID: fakeVoice}
	proc, err := tts.warm(t.Context(), voice)
	if err != nil {
		t.Fatalf("start voice: %v", err)
	}

	text := make(chan string, 1)
	text <- fakeHang
	close(text)
	audio, err := tts.Speak(context.Background(), "en", voice, text)
	if err != nil {
		t.Fatalf("Speak: %v", err)
	}
	waitStderrMarker(t, proc.stderr, fakeHangReady)

	if err = tts.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for range audio.Audio() {
		t.Fatal("hanging stream emitted audio")
	}
	if streamErr := audio.Err(); streamErr == nil {
		t.Fatal("active synthesis succeeded after Close")
	}
	select {
	case <-proc.done:
	default:
		t.Fatal("Close returned before the active helper was reaped")
	}
}

func TestTTSDistinctVoicesGetDistinctProcesses(t *testing.T) {
	t.Parallel()

	tts := newTestTTS(t, &TTSConfig{Bin: os.Args[0], Rate: 16000})

	speakOne(t, tts, core.Voice{ID: fakeVoice + "-a"}, "uno")
	speakOne(t, tts, core.Voice{ID: fakeVoice + "-b"}, "due")

	if got := procCount(tts); got != 2 {
		t.Fatalf("warm processes = %d, want 2 (one per voice)", got)
	}
}

func TestTTSCancelClosesOutputWhileWaitingForText(t *testing.T) {
	tts := newTestTTS(t, &TTSConfig{Bin: os.Args[0], Rate: 16000})
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	audio, err := tts.Speak(ctx, "en", core.Voice{ID: fakeVoice}, make(chan string))
	if err != nil {
		t.Fatalf("speak: %v", err)
	}
	for range audio.Audio() {
		t.Fatal("idle turn emitted audio")
	}
	if streamErr := audio.Err(); !errors.Is(streamErr, context.DeadlineExceeded) {
		t.Fatalf("stream error = %v, want deadline exceeded", streamErr)
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("context error = %v, want deadline exceeded", ctx.Err())
	}
}

func TestTTSHonorsContextOnReusedProcess(t *testing.T) {
	tts := newTestTTS(t, &TTSConfig{Bin: os.Args[0], Rate: 16000})
	voice := core.Voice{ID: fakeVoice}
	speakOne(t, tts, voice, "warm")

	old := voiceForTest(tts, voice.ID)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	text := make(chan string, 1)
	text <- fakeHang
	close(text)
	audio, err := tts.Speak(ctx, "en", voice, text)
	if err != nil {
		t.Fatalf("speak hanging clause: %v", err)
	}
	for range audio.Audio() {
		t.Fatal("hanging clause emitted audio")
	}
	if streamErr := audio.Err(); !errors.Is(streamErr, context.DeadlineExceeded) {
		t.Fatalf("stream error = %v, want deadline exceeded", streamErr)
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("context error = %v, want deadline exceeded", ctx.Err())
	}

	if got := speakOne(t, tts, voice, "restarted"); !reflect.DeepEqual(got, fakeSamples) {
		t.Fatalf("replacement PCM = %v, want %v", got, fakeSamples)
	}
	if replacement := voiceForTest(tts, voice.ID); replacement == old {
		t.Fatal("canceled helper remained cached")
	}
}

func TestTTSCanceledWaitDoesNotAbortActiveProcess(t *testing.T) {
	tts := newTestTTS(t, &TTSConfig{Bin: os.Args[0], Rate: 16000})
	voice := core.Voice{ID: fakeVoice}
	proc, err := tts.warm(t.Context(), voice)
	if err != nil {
		t.Fatalf("start voice: %v", err)
	}
	t.Cleanup(proc.abort)

	<-proc.gate
	held := true
	defer func() {
		if held {
			proc.gate <- struct{}{}
		}
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	text := make(chan string)
	close(text)
	audio, err := tts.Speak(ctx, "en", voice, text)
	if err != nil {
		t.Fatalf("speak queued turn: %v", err)
	}
	for range audio.Audio() {
		t.Fatal("queued turn emitted audio")
	}
	if streamErr := audio.Err(); !errors.Is(streamErr, context.DeadlineExceeded) {
		t.Fatalf("stream error = %v, want deadline exceeded", streamErr)
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("context error = %v, want deadline exceeded", ctx.Err())
	}
	if proc.unusable() {
		t.Fatal("canceling a queued turn stopped the active helper")
	}

	proc.gate <- struct{}{}
	held = false
	if got := speakOne(t, tts, voice, "still-warm"); !reflect.DeepEqual(got, fakeSamples) {
		t.Fatalf("PCM after queued cancellation = %v, want %v", got, fakeSamples)
	}
}

func TestTTSReplacesNaturallyExitedProcess(t *testing.T) {
	tts := newTestTTS(t, &TTSConfig{Bin: os.Args[0], Rate: 16000})
	voice := core.Voice{ID: fakeVoice}
	old, err := tts.warm(t.Context(), voice)
	if err != nil {
		t.Fatalf("start voice: %v", err)
	}

	if got := speakOne(t, tts, voice, fakeExitAfter); !reflect.DeepEqual(got, fakeSamples) {
		t.Fatalf("last PCM = %v, want %v", got, fakeSamples)
	}
	waitDone(t, old.done)

	if got := speakOne(t, tts, voice, "new-process"); !reflect.DeepEqual(got, fakeSamples) {
		t.Fatalf("replacement PCM = %v, want %v", got, fakeSamples)
	}
	if replacement := voiceForTest(tts, voice.ID); replacement == old {
		t.Fatal("exited helper remained cached")
	}
}

func TestTTSRejectsCorruptHelperOutput(t *testing.T) {
	for _, clause := range []string{fakeBadJSON, fakeBadBase64, fakeOddPCM, fakeOversized} {
		t.Run(clause, func(t *testing.T) {
			tts := newTestTTS(t, &TTSConfig{Bin: os.Args[0], Rate: 16000})

			got, streamErr := speakOneResult(t, tts, core.Voice{ID: fakeVoice}, clause)
			if len(got) != 0 {
				t.Fatalf("corrupt reply produced PCM %v", got)
			}
			if streamErr == nil {
				t.Fatal("corrupt reply reported a successful synthesis stream")
			}

			if got := speakOne(t, tts, core.Voice{ID: fakeVoice}, "recovered"); !reflect.DeepEqual(got, fakeSamples) {
				t.Fatalf("recovered PCM = %v, want %v", got, fakeSamples)
			}
		})
	}
}

func TestDecodeTTSResponseRequiresExactlyOneVariant(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		line    string
		wantErr bool
	}{
		{name: "audio", line: `{"audio":"AQI="}`},
		{name: "done", line: `{"done":true}`},
		{name: "empty object", line: `{}`, wantErr: true},
		{name: "empty audio", line: `{"audio":""}`, wantErr: true},
		{name: "false done", line: `{"done":false}`, wantErr: true},
		{name: "both", line: `{"audio":"AQI=","done":true}`, wantErr: true},
		{name: "null", line: `null`, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := decodeTTSResponse([]byte(test.line))
			if (err != nil) != test.wantErr {
				t.Fatalf("decodeTTSResponse(%s) error = %v, wantErr %v", test.line, err, test.wantErr)
			}
		})
	}
}

func TestTTSReportsProcessStderr(t *testing.T) {
	tts := newTestTTS(t, &TTSConfig{Bin: os.Args[0], Rate: 16000})

	got, streamErr := speakOneResult(t, tts, core.Voice{ID: fakeVoice}, fakeCrash)
	if len(got) != 0 {
		t.Fatalf("crashed helper produced PCM %v", got)
	}
	if streamErr == nil || !strings.Contains(streamErr.Error(), "fake tts crash") {
		t.Fatalf("stream error = %v, want helper stderr", streamErr)
	}
}

func TestTTSWriteFailureWaitsForHelperDiagnostic(t *testing.T) {
	proc, err := startVoiceProc(
		t.Context(), os.Args[0], fakeRejectTTS, 16000, discardTestLogger(),
	)
	if err != nil {
		t.Fatalf("start voice: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := proc.close(); closeErr != nil {
			t.Errorf("close voice: %v", closeErr)
		}
	})
	waitStderrMarker(t, proc.stderr, fakeTTSReject)

	started := time.Now()
	err = proc.synth(t.Context(), "hello", make(chan core.PCM, 1))
	if err == nil || !strings.Contains(err.Error(), fakeTTSReject) {
		t.Fatalf("synth error = %v, want helper stderr", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("synth took %v; failed helper was not stopped promptly", elapsed)
	}
}

func TestTTSRejectsCanceledContextBeforeSpawn(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	tts := newTestTTS(t, &TTSConfig{Bin: os.Args[0], Rate: 16000})
	_, err := tts.Speak(ctx, "en", core.Voice{ID: fakeVoice}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if got := procCount(tts); got != 0 {
		t.Fatalf("warm processes = %d, want none", got)
	}
}

func TestTTSRejectsVoiceLanguageMismatch(t *testing.T) {
	tts := newTestTTS(t, &TTSConfig{Bin: os.Args[0], Rate: 16000})
	voice := core.Voice{ID: fakeVoice, Lang: "it"}

	_, err := tts.Speak(t.Context(), "en", voice, nil)
	if err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("error = %v, want voice language mismatch", err)
	}
	if got := procCount(tts); got != 0 {
		t.Fatalf("warm processes = %d, want none", got)
	}
}

func voiceForTest(tts *TTS, voice string) *voiceProc {
	tts.mu.Lock()
	defer tts.mu.Unlock()

	return tts.procs[voice]
}

func procCount(tts *TTS) int {
	tts.mu.Lock()
	defer tts.mu.Unlock()

	return len(tts.procs)
}
