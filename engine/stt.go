package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	sttSilenceHang = 500 * time.Millisecond
	sttMaxWindow   = 9 * time.Second
	sttMinSpeech   = 250 * time.Millisecond
	sttVoicedRMS   = 0.012
	sttHTTPTimeout = 2 * time.Minute
	maxSTTThreads  = 64
)

func runSTT(args []string) (retErr error) {
	opts, err := parseSTTOptions(args)
	if err != nil {
		return err
	}

	dir := engineDir()
	server, base, err := startReadyWhisperServer(
		dir, bundlePath(dir, opts.model), opts.language, opts.threads,
	)
	if err != nil {
		return fmt.Errorf("stt: whisper-server not ready: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, server.stop()) }()

	client, transport := newWhisperHTTPClient(sttHTTPTimeout)
	defer transport.CloseIdleConnections()
	transcriber := &whisperSegmentTranscriber{
		client: client,
		out:    json.NewEncoder(os.Stdout),
		base:   base,
		lang:   opts.language,
		rate:   opts.rate,
	}

	return streamSTT(os.Stdin, opts.rate, transcriber.transcribe)
}

type sttOptions struct {
	model    string
	language string
	rate     int
	threads  int
}

func parseSTTOptions(args []string) (sttOptions, error) {
	opts := sttOptions{language: languageAuto, rate: 16000, threads: 1}
	flags := flag.NewFlagSet("stt", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.model, "model", "", "STT model path")
	flags.StringVar(&opts.language, "language", opts.language, "source language or auto")
	flags.IntVar(&opts.rate, "rate", opts.rate, "PCM sample rate")
	flags.IntVar(&opts.threads, "threads", opts.threads, "Whisper computation threads")
	if err := flags.Parse(args); err != nil {
		return sttOptions{}, fmt.Errorf("stt: parse flags: %w", err)
	}
	if flags.NArg() != 0 {
		return sttOptions{}, fmt.Errorf("stt: unexpected argument %q", flags.Arg(0))
	}
	if opts.model == "" {
		return sttOptions{}, errors.New("stt: --model is required")
	}
	if !validSampleRate(opts.rate) {
		return sttOptions{}, fmt.Errorf(
			"stt: --rate must be between %d and %d, got %d",
			minSampleRate, maxSampleRate, opts.rate,
		)
	}
	if opts.threads < 1 || opts.threads > maxSTTThreads {
		return sttOptions{}, fmt.Errorf(
			"stt: --threads must be between 1 and %d, got %d", maxSTTThreads, opts.threads,
		)
	}
	if !validLanguageArg(opts.language, true) {
		return sttOptions{}, fmt.Errorf("stt: invalid --language %q", opts.language)
	}
	// whisper's -l rejects region-qualified tags (it-CH), so pin it to the base
	// subtag; the transcript language stays base too, which is what MT pairs on.
	opts.language = baseLanguageTag(strings.ToLower(opts.language))

	return opts, nil
}

type whisperSegmentTranscriber struct {
	client *http.Client
	out    *json.Encoder
	base   string
	lang   string
	rate   int
}

func (t *whisperSegmentTranscriber) transcribe(pcm []int16) error {
	text, detected, err := whisperTranscribe(context.Background(), t.client, t.base, pcm, t.rate)
	if err != nil {
		return fmt.Errorf("stt: inference: %w", err)
	}
	text = strings.TrimSpace(text)
	// whisper marks near-silent windows with a bracketed non-speech token
	// ("[BLANK_AUDIO]", "(music)"); those must not enter the stream as speech.
	if text == "" || isNonSpeechPlaceholder(text) {
		return nil
	}

	return t.out.Encode(transcript{
		Text: text, Language: firstNonAuto(t.lang, detected), Final: true,
	})
}

// isNonSpeechPlaceholder reports whether text is entirely one of whisper's
// bracketed non-speech markers, rather than transcribed speech.
func isNonSpeechPlaceholder(text string) bool {
	if len(text) < 2 {
		return false
	}

	first, last := text[0], text[len(text)-1]

	return (first == '[' && last == ']') || (first == '(' && last == ')')
}

func streamSTT(input io.Reader, rate int, transcribe func([]int16) error) error {
	endpointer := &energyEndpointer{rate: rate}
	decoder := &s16StreamDecoder{}
	buf := make([]byte, 8192)

	for {
		n, readErr := input.Read(buf)
		if err := transcribeSegments(endpointer.push(decoder.decode(buf[:n])), transcribe); err != nil {
			return err
		}
		if readErr != nil {
			return finishSTT(readErr, decoder, endpointer, transcribe)
		}
	}
}

func transcribeSegments(segments [][]int16, transcribe func([]int16) error) error {
	for _, segment := range segments {
		if err := transcribe(segment); err != nil {
			return err
		}
	}

	return nil
}

func finishSTT(
	readErr error, decoder *s16StreamDecoder, endpointer *energyEndpointer, transcribe func([]int16) error,
) error {
	if decoder.pending {
		return errors.New("stt: truncated 16-bit PCM sample")
	}
	if segment := endpointer.flush(); len(segment) > 0 {
		if err := transcribe(segment); err != nil {
			return err
		}
	}
	if errors.Is(readErr, io.EOF) {
		return nil
	}

	return fmt.Errorf("stt: read: %w", readErr)
}

// s16StreamDecoder preserves a byte split across Read calls instead of
// dropping half of a PCM sample.
type s16StreamDecoder struct {
	scratch  []int16
	trailing byte
	pending  bool
}

// decode returns a view that remains valid until the next decode call. The STT
// loop passes it directly to energyEndpointer.push, which copies every sample.
//
//nolint:gosec // The conversion preserves the exact signed 16-bit PCM bit pattern.
func (d *s16StreamDecoder) decode(data []byte) []int16 {
	sampleCount := (len(data) + btoi(d.pending)) / 2
	if cap(d.scratch) < sampleCount {
		d.scratch = make([]int16, sampleCount)
	} else {
		d.scratch = d.scratch[:sampleCount]
	}
	samples := d.scratch
	index := 0
	if d.pending && len(data) > 0 {
		samples[0] = int16(binary.LittleEndian.Uint16([]byte{d.trailing, data[0]}))
		data = data[1:]
		index++
		d.pending = false
	}

	for len(data) >= 2 {
		samples[index] = int16(binary.LittleEndian.Uint16(data[:2]))
		data = data[2:]
		index++
	}
	if len(data) == 1 {
		d.trailing = data[0]
		d.pending = true
	}

	return samples[:index]
}

func btoi(value bool) int {
	if value {
		return 1
	}

	return 0
}

// firstNonAuto picks the first concrete language tag. Callers put a pinned
// hint first; auto-detection fills only an auto hint.
func firstNonAuto(tags ...string) string {
	for _, tag := range tags {
		if tag != "" && tag != languageAuto {
			return tag
		}
	}

	return ""
}

// transcript is one stdout line from the stt subcommand. The engine commits
// whole segments, so it emits only final transcripts; the wire protocol's
// partial form is produced by streaming backends, not this one.
type transcript struct {
	Text     string `json:"text,omitempty"`
	Language string `json:"language,omitempty"`
	Final    bool   `json:"final,omitempty"`
}

// energyEndpointer accumulates PCM and cuts a segment after trailing silence
// or at the window ceiling; it emits committed segments as []int16.
type energyEndpointer struct {
	buf        []int16
	rate       int
	voicedRun  time.Duration
	silenceRun time.Duration
	sawSpeech  bool
}

func (e *energyEndpointer) push(samples []int16) [][]int16 {
	var out [][]int16

	frame := e.rate / 50 // 20 ms
	if frame <= 0 {
		frame = 320
	}

	for len(samples) > 0 {
		take := min(frame, len(samples))
		chunk := samples[:take]
		samples = samples[take:]
		e.buf = append(e.buf, chunk...)

		dur := time.Duration(take) * time.Second / time.Duration(e.rate)
		if rms(chunk) >= sttVoicedRMS {
			e.sawSpeech = true
			e.voicedRun += dur
			e.silenceRun = 0
		} else {
			e.silenceRun += dur
		}

		bufDur := time.Duration(len(e.buf)) * time.Second / time.Duration(e.rate)
		endpoint := e.sawSpeech && e.voicedRun >= sttMinSpeech && e.silenceRun >= sttSilenceHang
		if endpoint || bufDur >= sttMaxWindow {
			if e.sawSpeech {
				out = append(out, e.take())
			} else {
				e.reset()
			}
		}
	}

	return out
}

func (e *energyEndpointer) flush() []int16 {
	if !e.sawSpeech {
		e.reset()

		return nil
	}

	return e.take()
}

func (e *energyEndpointer) take() []int16 {
	seg := append([]int16(nil), e.buf...)
	e.reset()

	return seg
}

func (e *energyEndpointer) reset() {
	e.buf = e.buf[:0]
	e.voicedRun, e.silenceRun, e.sawSpeech = 0, 0, false
}

func rms(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		v := float64(s) / 32768
		sum += v * v
	}

	return math.Sqrt(sum / float64(len(samples)))
}
