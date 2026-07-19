package speechengine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ubyte-source/prukka/internal/enginebundle"
	"github.com/ubyte-source/prukka/internal/nativewire"
)

const mtRequestTimeout = 2 * time.Minute

// RunMT serves the machine-translation stdio protocol over stdin/stdout,
// resolving the compiled translator and models from the engine bundle.
func RunMT(args []string) (err error) {
	from, to, err := parseMTOptions(args)
	if err != nil {
		return err
	}

	dir := engineDir()
	modelDir := filepath.Join(dir, enginebundle.MTModelDir(from, to))
	proc, err := startMTProc(dir, modelDir)
	if err != nil {
		return fmt.Errorf("mt: start translator: %w", err)
	}
	defer func() { err = errors.Join(err, proc.close()) }()

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64<<10), 1<<20)
	out := bufio.NewWriter(os.Stdout)

	for in.Scan() {
		text, err := decodeTextRequest(in.Bytes())
		if err != nil {
			return fmt.Errorf("mt: decode request: %w", err)
		}

		translated, terr := proc.translate(text)
		if terr != nil {
			return fmt.Errorf("mt: translate: %w", terr)
		}

		line, marshalErr := json.Marshal(nativewire.TextLine{Text: translated})
		if marshalErr != nil {
			return marshalErr
		}
		if _, err := out.Write(append(line, '\n')); err != nil {
			return err
		}
		if err := out.Flush(); err != nil {
			return err
		}
	}

	return in.Err()
}

func parseMTOptions(args []string) (from, to string, err error) {
	flags := flag.NewFlagSet("mt", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&from, "from", "", "source language")
	flags.StringVar(&to, "to", "", "target language")
	if err := flags.Parse(args); err != nil {
		return "", "", fmt.Errorf("mt: parse flags: %w", err)
	}
	if flags.NArg() != 0 {
		return "", "", fmt.Errorf("mt: unexpected argument %q", flags.Arg(0))
	}
	if !validLanguageArg(from, false) || !validLanguageArg(to, false) {
		return "", "", errors.New("mt: --from and --to must be concrete BCP-47 language tags")
	}

	return strings.ToLower(from), strings.ToLower(to), nil
}

// mtProc is the warm compiled translator: it holds the helper's pipes and
// serializes one source line to one translated line.
type mtProc struct {
	*stdioProc

	stdout io.ReadCloser
	out    *bufio.Scanner
}

// startMTProc spawns the compiled mt helper on the pair's model directory,
// which also holds the SentencePiece tokenizers beside the CT2 weights.
//
//nolint:gosec // The helper is bundle-resolved and receives tokenized arguments without a shell.
func startMTProc(dir, modelDir string) (*mtProc, error) {
	cmd := exec.CommandContext(context.Background(), filepath.Join(dir, "mt"),
		"--model", modelDir,
		"--source-spm", filepath.Join(modelDir, "source.spm"),
		"--target-spm", filepath.Join(modelDir, "target.spm"))
	cmd.Env = libraryEnv(os.Environ(), libDir(dir))
	cmd.Stderr = os.Stderr

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
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)

	return &mtProc{stdioProc: &stdioProc{cmd: cmd, stdin: stdin}, stdout: stdout, out: scanner}, nil
}

// translate sends one source line and reads its translation. The helper speaks
// a strict one-line-in, one-line-out protocol, so blank input is answered
// locally without perturbing that lockstep.
func (p *mtProc) translate(text string) (string, error) {
	return p.translateWithin(text, mtRequestTimeout)
}

func (p *mtProc) translateWithin(text string, timeout time.Duration) (string, error) {
	clean := strings.ReplaceAll(strings.ReplaceAll(text, "\r", " "), "\n", " ")
	if strings.TrimSpace(clean) == "" {
		return "", nil
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	if err := p.writeTranslationRequest(clean+"\n", timer.C); err != nil {
		return "", err
	}

	return p.readTranslationReply(timer.C)
}

func (p *mtProc) writeTranslationRequest(line string, timeout <-chan time.Time) error {
	writeDone := make(chan error, 1)
	go func() {
		n, err := io.WriteString(p.stdin, line)
		if err == nil && n != len(line) {
			err = io.ErrShortWrite
		}
		writeDone <- err
	}()

	select {
	case err := <-writeDone:
		return err
	case <-timeout:
		return errors.Join(errors.New("translator request write timed out"), p.abort())
	}
}

func (p *mtProc) readTranslationReply(timeout <-chan time.Time) (string, error) {
	readDone := make(chan mtReply, 1)
	go func() {
		if p.out.Scan() {
			readDone <- mtReply{text: strings.TrimSpace(p.out.Text())}

			return
		}
		err := p.out.Err()
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		readDone <- mtReply{err: err}
	}()

	select {
	case reply := <-readDone:
		return reply.text, reply.err
	case <-timeout:
		return "", errors.Join(errors.New("translator response timed out"), p.abort())
	}
}

type mtReply struct {
	err  error
	text string
}

func (p *mtProc) abort() error {
	var stdoutErr error
	if p.stdout != nil {
		stdoutErr = p.stdout.Close()
		if errors.Is(stdoutErr, os.ErrClosed) {
			stdoutErr = nil
		}
	}

	return errors.Join(p.kill(), p.closeInput(), stdoutErr)
}

// close tears the helper down when the pair's stream ends.
func (p *mtProc) close() error {
	closeErr := p.closeInput()
	killErr := p.kill()
	var stdoutErr error
	if p.stdout != nil {
		stdoutErr = p.stdout.Close()
		if errors.Is(stdoutErr, os.ErrClosed) {
			stdoutErr = nil
		}
	}

	waitErr := p.cmd.Wait()
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		waitErr = nil // a killed helper exits by signal; cleanup succeeded
	}

	return errors.Join(closeErr, killErr, stdoutErr, waitErr)
}
