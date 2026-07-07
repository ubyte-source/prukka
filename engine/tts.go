package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	ttsTextMaxBytes     = 64 << 10
	ttsWAVMaxBytes      = 64 << 20
	ttsAudioChunkBytes  = 32 << 10
	piperStopGrace      = 2 * time.Second
	piperRequestTimeout = 2 * time.Minute
	piperWAVReadyWindow = 2 * time.Second
)

func runTTS(args []string) (err error) {
	voice, rate, err := parseTTSOptions(args)
	if err != nil {
		return err
	}
	dir := engineDir()
	voice = bundlePath(dir, voice)

	workDir, err := os.MkdirTemp("", "prukka-piper-")
	if err != nil {
		return fmt.Errorf("tts: create private work directory: %w", err)
	}
	defer func() { err = errors.Join(err, os.RemoveAll(workDir)) }()

	synth, err := startPiperProc(dir, voice, workDir)
	if err != nil {
		return fmt.Errorf("tts: start Piper: %w", err)
	}
	defer func() { err = errors.Join(err, synth.close()) }()

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64<<10), 1<<20)
	out := bufio.NewWriter(os.Stdout)
	encoder := json.NewEncoder(out)

	for in.Scan() {
		if err := synthesizeLine(synth, rate, in.Bytes(), encoder, out); err != nil {
			return err
		}
	}

	return in.Err()
}

func parseTTSOptions(args []string) (voice string, rate int, err error) {
	rate = 16000
	flags := flag.NewFlagSet("tts", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&voice, "model", "", "TTS voice model path")
	flags.IntVar(&rate, "rate", rate, "PCM sample rate")
	if err := flags.Parse(args); err != nil {
		return "", 0, fmt.Errorf("tts: parse flags: %w", err)
	}
	if flags.NArg() != 0 {
		return "", 0, fmt.Errorf("tts: unexpected argument %q", flags.Arg(0))
	}
	if voice == "" {
		return "", 0, errors.New("tts: --model is required")
	}
	if !validSampleRate(rate) {
		return "", 0, fmt.Errorf(
			"tts: --rate must be between %d and %d, got %d",
			minSampleRate, maxSampleRate, rate,
		)
	}

	return voice, rate, nil
}

func decodeTextRequest(line []byte) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return "", err
	}
	if len(fields) != 1 {
		return "", errors.New("request must contain only the text field")
	}
	raw, ok := fields["text"]
	if !ok {
		return "", errors.New("request is missing the text field")
	}

	var text *string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", fmt.Errorf("text must be a string: %w", err)
	}
	if text == nil {
		return "", errors.New("text must not be null")
	}

	return *text, nil
}

type ttsSynthesizer interface {
	synthesize(text string) ([]int16, int, error)
}

func synthesizeLine(synth ttsSynthesizer, rate int, line []byte, encoder *json.Encoder, out *bufio.Writer) error {
	text, err := decodeTextRequest(line)
	if err != nil {
		return fmt.Errorf("tts: decode request: %w", err)
	}
	if len(text) > ttsTextMaxBytes {
		return fmt.Errorf("tts: text exceeds %d bytes", ttsTextMaxBytes)
	}
	if strings.TrimSpace(text) == "" {
		return writeTTSDone(encoder, out)
	}

	pcm, voiceRate, err := synth.synthesize(text)
	if err != nil {
		return fmt.Errorf("tts: piper: %w", err)
	}
	if voiceRate != rate {
		pcm = resampleLinear(pcm, voiceRate, rate)
	}

	const chunkSamples = ttsAudioChunkBytes / 2
	for len(pcm) > 0 {
		count := min(len(pcm), chunkSamples)
		raw := int16ToBytes(pcm[:count])
		if err := encoder.Encode(struct {
			Audio string `json:"audio"`
		}{Audio: base64.StdEncoding.EncodeToString(raw)}); err != nil {
			return err
		}
		pcm = pcm[count:]
	}
	return writeTTSDone(encoder, out)
}

func writeTTSDone(encoder *json.Encoder, out *bufio.Writer) error {
	if err := encoder.Encode(struct {
		Done bool `json:"done"`
	}{Done: true}); err != nil {
		return err
	}

	return out.Flush()
}

// piperProc keeps one loaded voice and uses Piper's one-JSON-line/one-WAV
// protocol. Raw stdout is deliberately avoided because it has no request
// boundary or acknowledgement in the pinned Piper release.
type piperProc struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	input     *json.Encoder
	acks      <-chan piperAck
	done      chan struct{}
	waitErr   error
	stdinErr  error
	workDir   string
	stdinOnce sync.Once
}

type piperAck struct {
	err  error
	path string
}

//nolint:gosec // The helper is bundle-resolved and receives tokenized arguments without a shell.
func startPiperProc(dir, voice, workDir string) (*piperProc, error) {
	cmd := exec.CommandContext(context.Background(), filepath.Join(dir, "piper", "piper"),
		"--model", voice, "--json-input", "--quiet")
	cmd.Env = libraryEnv(os.Environ(), filepath.Join(dir, "piper"))
	cmd.Stderr = os.Stderr

	return startPiperCommand(cmd, workDir)
}

func startPiperCommand(cmd *exec.Cmd, workDir string) (*piperProc, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errors.Join(err, stdin.Close())
	}
	if err := cmd.Start(); err != nil {
		return nil, errors.Join(err, stdin.Close(), stdout.Close())
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	acks := make(chan piperAck, 1)
	proc := &piperProc{
		cmd: cmd, stdin: stdin, input: json.NewEncoder(stdin), acks: acks,
		done: make(chan struct{}), workDir: workDir,
	}
	go pumpPiperAcks(scanner, acks)
	go func() {
		proc.waitErr = cmd.Wait()
		close(proc.done)
	}()

	return proc, nil
}

func pumpPiperAcks(scanner *bufio.Scanner, acks chan<- piperAck) {
	defer close(acks)
	for scanner.Scan() {
		acks <- piperAck{path: scanner.Text()}
	}
	if err := scanner.Err(); err != nil {
		acks <- piperAck{err: err}
	}
}

func (p *piperProc) synthesize(text string) (samples []int16, rate int, err error) {
	file, err := os.CreateTemp(p.workDir, "audio-*.wav")
	if err != nil {
		return nil, 0, fmt.Errorf("reserve output: %w", err)
	}
	path := file.Name()
	if closeErr := file.Close(); closeErr != nil {
		return nil, 0, errors.Join(closeErr, os.Remove(path))
	}
	defer func() { err = errors.Join(err, os.Remove(path)) }()

	if err := p.request(text, path); err != nil {
		return nil, 0, err
	}

	return waitForPCM16WAV(path, piperWAVReadyWindow)
}

func (p *piperProc) request(text, path string) error {
	return p.requestWithin(text, path, piperRequestTimeout)
}

func (p *piperProc) requestWithin(text, path string, timeout time.Duration) error {
	request := struct {
		Text       string `json:"text"`
		OutputFile string `json:"output_file"`
	}{Text: text, OutputFile: path}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	writeDone := make(chan error, 1)
	go func() {
		encodeErr := p.input.Encode(request)
		writeDone <- encodeErr
	}()

	select {
	case err := <-writeDone:
		if err != nil {
			return fmt.Errorf("write request: %w", err)
		}
	case <-timer.C:
		return errors.Join(errors.New("piper request write timed out"), p.abort())
	}

	select {
	case acknowledgement, ok := <-p.acks:
		if !ok {
			return fmt.Errorf("read acknowledgement: %w", io.ErrUnexpectedEOF)
		}
		if acknowledgement.err != nil {
			return fmt.Errorf("read acknowledgement: %w", acknowledgement.err)
		}
		if acknowledgement.path != path {
			return fmt.Errorf("acknowledged path %q, want %q", acknowledgement.path, path)
		}
	case <-timer.C:
		return errors.Join(errors.New("piper acknowledgement timed out"), p.abort())
	}

	return nil
}

func (p *piperProc) abort() error {
	return errors.Join(p.kill(), p.closeInput())
}

func (p *piperProc) closeInput() error {
	p.stdinOnce.Do(func() { p.stdinErr = p.stdin.Close() })

	return p.stdinErr
}

func (p *piperProc) kill() error {
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	err := p.cmd.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}

	return err
}

func (p *piperProc) close() error {
	closeErr := p.closeInput()
	timer := time.NewTimer(piperStopGrace)
	defer timer.Stop()

	select {
	case <-p.done:
		return errors.Join(closeErr, p.waitErr)
	case <-timer.C:
	}

	killErr := p.kill()
	<-p.done

	return errors.Join(closeErr, errors.New("piper did not stop after stdin closed"), killErr, p.waitErr)
}
