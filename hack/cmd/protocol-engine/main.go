// Command protocol-engine is a deterministic implementation of the local
// speech-engine stdio contract for CI and media-pipeline acceptance tests.
//
// The flag accept-set (protocol version, tuning bounds) comes from
// internal/nativewire — the contract both real ends share — so the double
// cannot accept an invocation the real engine would refuse. The wire frames
// stay hand-written on purpose: the demo gate must exercise the daemon's
// encoding, not share it.
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/nativewire"
)

const (
	defaultLanguage   = "it"
	defaultRate       = 16000
	defaultSTTThreads = 1
	maxSTTThreads     = 64
	subcommandSTT     = nativewire.SubSTT
	// The engine's sample-rate band lives unexported in
	// internal/speechengine/common.go; these mirror it so the double rejects
	// the same rates. Single-sourcing it would need the predicate moved into
	// nativewire beside STTTuning.
	minSampleRate = 8000
	maxSampleRate = 192000
)

// validSampleRate mirrors internal/speechengine's rate check for both the
// stt and tts lanes.
func validSampleRate(rate int) bool {
	return rate >= minSampleRate && rate <= maxSampleRate
}

func main() {
	os.Exit(realMain(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func realMain(args []string, in io.Reader, out, stderr io.Writer) int {
	if len(args) == 0 {
		if _, err := fmt.Fprintln(stderr, "usage: protocol-engine stt|mt|tts [flags]"); err != nil {
			return 2
		}

		return 2
	}

	var err error
	switch args[0] {
	case subcommandSTT:
		err = runSTT(args[1:], in, out)
	case nativewire.SubMT:
		err = runMT(args[1:], in, out)
	case nativewire.SubTTS:
		err = runTTS(args[1:], in, out)
	default:
		err = fmt.Errorf("unknown subcommand %q", args[0])
	}
	if err != nil {
		if _, writeErr := fmt.Fprintln(stderr, err); writeErr != nil {
			return 1
		}

		return 1
	}

	return 0
}

func runSTT(args []string, in io.Reader, out io.Writer) error {
	config, err := parseSTTConfig(args)
	if err != nil {
		return err
	}

	encoder := json.NewEncoder(out)
	if encodeErr := encoder.Encode(struct {
		Ready bool `json:"ready"`
	}{Ready: true}); encodeErr != nil {
		return fmt.Errorf("encode STT readiness: %w", encodeErr)
	}
	bytesRead, err := io.Copy(io.Discard, in)
	if err != nil {
		return fmt.Errorf("read PCM: %w", err)
	}
	if bytesRead%2 != 0 {
		return errors.New("read PCM: truncated 16-bit sample")
	}

	return encodeSTTResult(encoder, config.language, bytesRead/2)
}

type sttConfig struct {
	model    string
	language string
	tuning   nativewire.STTTuning
	protocol int
	rate     int
	threads  int
}

func parseSTTConfig(args []string) (sttConfig, error) {
	flags := flag.NewFlagSet(subcommandSTT, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	config := sttConfig{}
	flags.StringVar(&config.model, nativewire.FlagModel, "", "model path")
	flags.IntVar(&config.protocol, nativewire.FlagProtocol, 0, "daemon/helper protocol version")
	flags.StringVar(&config.language, nativewire.FlagLanguage, defaultLanguage, "source language")
	flags.IntVar(&config.rate, nativewire.FlagRate, defaultRate, "sample rate")
	flags.IntVar(&config.threads, nativewire.FlagThreads, defaultSTTThreads, "Whisper computation threads")
	flags.DurationVar(&config.tuning.SilenceHang, nativewire.FlagSilenceHang,
		300*time.Millisecond, "trailing silence endpoint")
	flags.DurationVar(&config.tuning.MaxWindow, nativewire.FlagMaxWindow,
		5*time.Second, "maximum STT window")
	flags.DurationVar(&config.tuning.MinSpeech, nativewire.FlagMinSpeech,
		250*time.Millisecond, "minimum voiced audio")
	_ = flags.Bool(nativewire.FlagFastDecode, false, "bounded-context conversational decode")
	if err := flags.Parse(args); err != nil {
		return sttConfig{}, fmt.Errorf("stt flags: %w", err)
	}
	if err := config.validate(); err != nil {
		return sttConfig{}, err
	}

	return config, nil
}

func (c *sttConfig) validate() error {
	if c.model == "" {
		return errors.New("stt requires --model")
	}
	if !validSampleRate(c.rate) {
		return fmt.Errorf("stt requires --rate between %d and %d", minSampleRate, maxSampleRate)
	}
	if c.protocol != nativewire.ProtocolVersion {
		return fmt.Errorf("stt requires --protocol-version %d", nativewire.ProtocolVersion)
	}
	if c.threads < 1 || c.threads > maxSTTThreads {
		return fmt.Errorf("stt requires --threads between 1 and %d", maxSTTThreads)
	}
	if err := c.tuning.Validate(); err != nil {
		return fmt.Errorf("stt: %w", err)
	}

	return nil
}

func encodeSTTResult(encoder *json.Encoder, language string, endSamples int64) error {
	lang := language
	if lang == "" || lang == "auto" {
		lang = defaultLanguage
	}

	return encoder.Encode(struct {
		Text       string `json:"text"`
		Language   string `json:"language"`
		Final      bool   `json:"final"`
		EndSamples int64  `json:"end_samples"`
	}{
		Text: "ciao dal motore di protocollo", Language: lang,
		Final: true, EndSamples: endSamples,
	})
}

func runMT(args []string, in io.Reader, out io.Writer) error {
	flags := flag.NewFlagSet("mt", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	from := flags.String(nativewire.FlagFrom, "", "source language")
	to := flags.String(nativewire.FlagTo, "", "target language")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("mt flags: %w", err)
	}
	if *from == "" || *to == "" {
		return errors.New("mt requires --from and --to")
	}

	scanner := bufio.NewScanner(in)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		var request struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			return fmt.Errorf("decode MT request: %w", err)
		}
		if err := encoder.Encode(struct {
			Text string `json:"text"`
		}{Text: "[" + *to + "] " + request.Text}); err != nil {
			return fmt.Errorf("encode MT response: %w", err)
		}
	}

	return scanner.Err()
}

func runTTS(args []string, in io.Reader, out io.Writer) error {
	flags := flag.NewFlagSet("tts", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	model := flags.String(nativewire.FlagModel, "", "voice model")
	rate := flags.Int(nativewire.FlagRate, defaultRate, "sample rate")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("tts flags: %w", err)
	}
	if *model == "" {
		return errors.New("tts requires --model")
	}
	if !validSampleRate(*rate) {
		return fmt.Errorf("tts requires --rate between %d and %d", minSampleRate, maxSampleRate)
	}

	encoder := json.NewEncoder(out)
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		var request struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			return fmt.Errorf("decode TTS request: %w", err)
		}
		if request.Text == "" {
			continue
		}
		if err := writeTone(encoder, *rate); err != nil {
			return err
		}
	}

	return scanner.Err()
}

func writeTone(encoder *json.Encoder, rate int) error {
	samples := make([]int16, rate/4) // 250 ms of mono audio.
	value := int16(4000)
	phase := 0
	for sample := range samples {
		samples[sample] = value
		phase += 880
		if phase >= rate {
			phase -= rate
			value = -value
		}
	}
	raw := pipeline.EncodeS16LE(samples)
	if err := encoder.Encode(struct {
		Audio string `json:"audio"`
	}{Audio: base64.StdEncoding.EncodeToString(raw)}); err != nil {
		return fmt.Errorf("encode TTS audio: %w", err)
	}
	if err := encoder.Encode(struct {
		Done bool `json:"done"`
	}{Done: true}); err != nil {
		return fmt.Errorf("encode TTS boundary: %w", err)
	}

	return nil
}
