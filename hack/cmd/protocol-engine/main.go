// Command protocol-engine is a deterministic implementation of the local
// speech-engine stdio contract for CI and media-pipeline acceptance tests.
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

	"github.com/ubyte-source/prukka/internal/core/pipeline"
)

const (
	defaultLanguage   = "it"
	defaultRate       = 16000
	defaultSTTThreads = 1
	maxSTTThreads     = 64
	subcommandSTT     = "stt"
)

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
	case "mt":
		err = runMT(args[1:], in, out)
	case "tts":
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
	flags := flag.NewFlagSet(subcommandSTT, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	model := flags.String("model", "", "model path")
	language := flags.String("language", defaultLanguage, "source language")
	rate := flags.Int("rate", defaultRate, "sample rate")
	threads := flags.Int("threads", defaultSTTThreads, "Whisper computation threads")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("stt flags: %w", err)
	}
	if *model == "" || *rate <= 0 {
		return errors.New("stt requires --model and a positive --rate")
	}
	if *threads < 1 || *threads > maxSTTThreads {
		return fmt.Errorf("stt requires --threads between 1 and %d", maxSTTThreads)
	}
	if _, err := io.Copy(io.Discard, in); err != nil {
		return fmt.Errorf("read PCM: %w", err)
	}
	lang := *language
	if lang == "" || lang == "auto" {
		lang = defaultLanguage
	}

	return json.NewEncoder(out).Encode(struct {
		Text     string `json:"text"`
		Language string `json:"language"`
		Final    bool   `json:"final"`
	}{Text: "ciao dal motore di protocollo", Language: lang, Final: true})
}

func runMT(args []string, in io.Reader, out io.Writer) error {
	flags := flag.NewFlagSet("mt", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	from := flags.String("from", "", "source language")
	to := flags.String("to", "", "target language")
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
	model := flags.String("model", "", "voice model")
	rate := flags.Int("rate", defaultRate, "sample rate")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("tts flags: %w", err)
	}
	if *model == "" || *rate <= 0 {
		return errors.New("tts requires --model and a positive --rate")
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
