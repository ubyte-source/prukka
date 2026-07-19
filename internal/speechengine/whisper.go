package speechengine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ubyte-source/prukka/internal/enginebundle"
)

const (
	contentTypeJSON     = "application/json"
	whisperLoopbackHost = "127.0.0.1"
	// callAudioContext covers 10.24 s of Whisper mel context (one encoder
	// position spans 20 ms). Keep comfortable headroom above the 5 s call
	// endpoint: running near the context edge can force decoder fallbacks and
	// turn a nominally fast request into a timeout.
	callAudioContext    = 512
	whisperTextMaxBytes = 4 << 10
)

// errUnsafeWhisperTranscript marks a successful inference whose text must not
// leave the native helper. It is deliberately distinct from transport/model
// failures: one malformed decoder result is a droppable media event, not a
// reason to tear down the long-lived STT lane and its device sink.
var errUnsafeWhisperTranscript = errors.New("unsafe whisper transcript")

// childProcess owns the only Wait call for a spawned helper.
type childProcess struct {
	cmd      *exec.Cmd
	done     chan struct{}
	waitErr  error
	stopErr  error
	stopOnce sync.Once
}

//nolint:gosec // The helper is bundle-resolved and receives tokenized arguments without a shell.
func startWhisperServer(
	dir, model, sourceLang string, threads int, fastDecode bool,
) (*childProcess, string, error) {
	requestPath, err := newWhisperRequestPath()
	if err != nil {
		return nil, "", fmt.Errorf("stt: create whisper-server request path: %w", err)
	}

	port, err := freeLoopbackPort()
	if err != nil {
		return nil, "", fmt.Errorf("stt: reserve whisper-server port: %w", err)
	}

	server := exec.CommandContext(
		context.Background(), filepath.Join(dir, enginebundle.WhisperServer),
		whisperServerArgs(model, sourceLang, threads, port, requestPath, fastDecode)...,
	)
	server.Env = libraryEnv(os.Environ(), libDir(dir))
	server.Stderr = os.Stderr
	if err := server.Start(); err != nil {
		return nil, "", fmt.Errorf("stt: start whisper-server: %w", err)
	}

	child := &childProcess{cmd: server, done: make(chan struct{})}
	go func() {
		child.waitErr = server.Wait()
		close(child.done)
	}()

	return child, "http://" + whisperLoopbackHost + ":" + port + requestPath, nil
}

const (
	whisperStartAttempts = 3
	// whisperReadyTimeout is one total startup budget across retries. A server
	// that loses the reserved loopback port exits promptly, leaving the rest of
	// the budget for a fresh bind without exceeding the outer 30 s handshake.
	whisperReadyTimeout = 25 * time.Second
)

// startReadyWhisperServer starts whisper-server and waits until it serves.
// freeLoopbackPort closes the reserved port before the server binds it, so a
// racing process can take it and the server exits early; each such loss retries
// on a fresh port rather than failing the session.
func startReadyWhisperServer(
	dir, model, sourceLang string, threads int, fastDecode bool,
) (*childProcess, string, error) {
	var lastErr error
	deadline := time.Now().Add(whisperReadyTimeout)

	for range whisperStartAttempts {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		server, base, err := startWhisperServer(dir, model, sourceLang, threads, fastDecode)
		if err != nil {
			lastErr = err

			continue
		}
		if readyErr := waitReady(base, remaining, server); readyErr != nil {
			lastErr = errors.Join(readyErr, server.stop())

			continue
		}

		return server, base, nil
	}

	return nil, "", lastErr
}

func whisperServerArgs(
	model, sourceLang string, threads int, port, requestPath string, fastDecode bool,
) []string {
	args := []string{
		"-m", model,
		"-l", sourceLang,
		"-t", strconv.Itoa(threads),
	}
	if fastDecode {
		// A bounded audio context covers the 5 s call endpoint window while
		// avoiding Whisper's fixed 30 s encoder work, and greedy best-of-one
		// keeps decoding latency deterministic. Keep token timestamps enabled:
		// whisper.cpp's no-timestamps mode combined with a bounded audio context
		// can poison the next decode after an HTTP request is canceled, making
		// it repeat the same sentence until the context is full.
		args = append(args, "-bo", "1", "-ac", strconv.Itoa(callAudioContext))
	}

	return append(args,
		"--host", whisperLoopbackHost,
		"--port", port,
		"--request-path", requestPath,
	)
}

func newWhisperRequestPath() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}

	return "/prukka-" + hex.EncodeToString(token[:]), nil
}

func freeLoopbackPort() (string, error) {
	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(
		context.Background(), "tcp", net.JoinHostPort(whisperLoopbackHost, "0"),
	)
	if err != nil {
		return "", err
	}

	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return "", errors.Join(err, listener.Close())
	}
	if closeErr := listener.Close(); closeErr != nil {
		return "", closeErr
	}

	return port, nil
}

func (p *childProcess) stop() error {
	p.stopOnce.Do(func() {
		killErr := p.cmd.Process.Kill()
		killed := killErr == nil

		<-p.done
		waitErr := p.waitErr
		if killed {
			// We killed a live child; its "signal: killed" wait error is expected.
			waitErr = nil
		} else {
			// Kill failed because the child had already exited (ErrProcessDone on
			// POSIX, EINVAL on Windows). <-p.done proves it is reaped, so the real
			// exit status lives in waitErr and the stale kill error is dropped.
			killErr = nil
		}
		p.stopErr = errors.Join(killErr, waitErr)
	})

	return p.stopErr
}

// whisperTranscribe posts one PCM segment as a WAV to the whisper server and
// returns the source-language transcript plus the language whisper detected.
func whisperTranscribe(
	ctx context.Context, client *http.Client, base string, pcm []int16, rate int,
	options ...whisperInferenceOptions,
) (text, language string, err error) {
	req, err := whisperRequest(ctx, base, encodeWAV(pcm, rate), options...)
	if err != nil {
		return "", "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()

	text, language, err = decodeWhisperResponse(resp)
	if err != nil {
		return "", "", err
	}
	if err := validateWhisperTranscript(text); err != nil {
		return "", "", err
	}

	return text, language, nil
}

// validateWhisperTranscript is a final fail-closed boundary before text can
// fan out into translation and synthesis. Besides a hard size cap, it rejects
// the characteristic adjacent n-gram loop produced by a poisoned decoder;
// allowing that loop through can synthesize minutes of audio from one bounded
// call window and permanently bury live speech behind stale playout.
func validateWhisperTranscript(text string) error {
	if len(text) > whisperTextMaxBytes {
		return fmt.Errorf(
			"%w: whisper-server transcript exceeds %d bytes",
			errUnsafeWhisperTranscript, whisperTextMaxBytes,
		)
	}
	if hasPathologicalTokenRepetition(text) {
		return fmt.Errorf(
			"%w: whisper-server transcript contains pathological repetition",
			errUnsafeWhisperTranscript,
		)
	}

	return nil
}

func hasPathologicalTokenRepetition(text string) bool {
	const (
		minimumRepetitions   = 4
		minimumRepeatedWords = 16
		maximumPatternWords  = 64
	)

	words := strings.FieldsFunc(strings.ToLower(text), func(char rune) bool {
		return unicode.IsSpace(char) || unicode.IsPunct(char)
	})
	for start := range words {
		maxPattern := min(maximumPatternWords, (len(words)-start)/minimumRepetitions)
		for width := 1; width <= maxPattern; width++ {
			pattern := words[start : start+width]
			repetitions := 1
			for next := start + width; next+width <= len(words); next += width {
				if !slices.Equal(pattern, words[next:next+width]) {
					break
				}
				repetitions++
			}
			if repetitions >= minimumRepetitions && repetitions*width >= minimumRepeatedWords {
				return true
			}
		}
	}

	return false
}

type whisperInferenceOptions struct {
	fastDecode bool
}

func whisperRequest(
	ctx context.Context, base string, wav []byte, options ...whisperInferenceOptions,
) (*http.Request, error) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := writeWhisperForm(form, wav, options...); err != nil {
		return nil, err
	}

	inferenceURL, err := loopbackWhisperEndpoint(base, "/inference")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, inferenceURL, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())

	return req, nil
}

func writeWhisperForm(
	form *multipart.Writer, wav []byte, options ...whisperInferenceOptions,
) error {
	part, err := form.CreateFormFile("file", "seg.wav")
	if err != nil {
		return err
	}
	if _, writeErr := part.Write(wav); writeErr != nil {
		return writeErr
	}
	if fieldErr := form.WriteField("response_format", "json"); fieldErr != nil {
		return fieldErr
	}
	if fieldErr := form.WriteField("temperature", "0"); fieldErr != nil {
		return fieldErr
	}
	if len(options) != 0 && options[0].fastDecode {
		// The pinned whisper-server parses --no-fallback but does not apply it.
		// A zero temperature increment is its effective one-pass equivalent.
		if fieldErr := form.WriteField("temperature_inc", "0"); fieldErr != nil {
			return fieldErr
		}
	}
	return form.Close()
}

func decodeWhisperResponse(resp *http.Response) (text, language string, err error) {
	if envelopeErr := validateWhisperInferenceEnvelope(resp); envelopeErr != nil {
		return "", "", envelopeErr
	}

	const maxResponse = 4 << 20

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return "", "", err
	}
	if len(raw) > maxResponse {
		return "", "", errors.New("whisper-server response exceeds 4 MiB")
	}

	declaredJSON := responseIsJSON(resp.Header.Get("Content-Type"))
	trimmed := bytes.TrimSpace(raw)
	looksLikeObject := len(trimmed) != 0 && trimmed[0] == '{'
	if declaredJSON || looksLikeObject {
		// A body that was declared JSON or looks like a JSON object must BE
		// valid JSON; a malformed object is a server fault, never a transcript.
		if !json.Valid(raw) {
			return "", "", errors.New("whisper-server returned invalid JSON")
		}

		return decodeWhisperJSON(raw)
	}

	// Older server builds answer text/plain even when asked for json.
	return string(raw), "", nil
}

func validateWhisperInferenceEnvelope(resp *http.Response) error {
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("whisper-server: %s", resp.Status)
	}

	return validateWhisperServerHeader(resp.Header)
}

func decodeWhisperJSON(raw []byte) (text, language string, err error) {
	var parsed struct {
		Text     *string `json:"text"`
		Language string  `json:"language"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", "", fmt.Errorf("whisper-server JSON shape: %w", err)
	}
	if parsed.Text == nil {
		return "", "", errors.New("whisper-server JSON shape: missing string field text")
	}
	parsed.Language = strings.ToLower(strings.TrimSpace(parsed.Language))
	if parsed.Language != "" && !validLanguageArg(parsed.Language, false) {
		return "", "", fmt.Errorf(
			"whisper-server JSON shape: invalid BCP-47 language %q", parsed.Language,
		)
	}

	return *parsed.Text, parsed.Language, nil
}

func responseIsJSON(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)

	return err == nil && (mediaType == contentTypeJSON || strings.HasSuffix(mediaType, "+json"))
}

const (
	whisperHealthMaxBytes = 64
	whisperHealthLoading  = `{"status":"loading model"}`
	whisperHealthReady    = `{"status":"ok"}`
	whisperServerName     = "whisper.cpp"
)

var errWhisperRedirect = errors.New("whisper-server redirect refused")

func waitReady(base string, timeout time.Duration, process *childProcess) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	healthURL, err := loopbackHealthURL(base)
	if err != nil {
		return err
	}
	client, transport := newWhisperHTTPClient(time.Second)
	defer transport.CloseIdleConnections()
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()

	for {
		if err := process.exitedError(); err != nil {
			return err
		}
		ready, err := probeWhisperHealth(ctx, client, healthURL)
		if err != nil {
			return err
		}
		if ready {
			return process.exitedError()
		}
		if err := waitForWhisperPoll(ctx, ticker.C, process); err != nil {
			return err
		}
	}
}

func probeWhisperHealth(ctx context.Context, client *http.Client, healthURL string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, http.NoBody)
	if err != nil {
		return false, err
	}
	resp, err := client.Do(req)
	if errors.Is(err, errWhisperRedirect) {
		return false, errWhisperRedirect
	}
	if err != nil {
		// The server has not bound the port yet; keep polling. Swallowing this
		// expected startup dial error must live in its own function, or nilerr
		// flags returning a nil error while err is non-nil here.
		return whisperNotReady()
	}

	return validateWhisperHealth(resp)
}

// whisperNotReady reports a not-yet-listening server as a clean not-ready
// result for the poll loop.
func whisperNotReady() (bool, error) {
	return false, nil
}

func newWhisperHTTPClient(timeout time.Duration) (*http.Client, *http.Transport) {
	dialer := &net.Dialer{Timeout: time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialWhisperLoopback(ctx, dialer, network, address)
		},
		DisableCompression:     true,
		MaxResponseHeaderBytes: 4 << 10,
		Proxy:                  nil,
	}
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errWhisperRedirect
		},
		Timeout:   timeout,
		Transport: transport,
	}

	return client, transport
}

func dialWhisperLoopback(
	ctx context.Context, dialer *net.Dialer, network, address string,
) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parse whisper-server address: %w", err)
	}
	if host != whisperLoopbackHost || port == "" {
		return nil, errors.New("whisper-server client refused a non-loopback address")
	}

	return dialer.DialContext(ctx, network, address)
}

func waitForWhisperPoll(ctx context.Context, tick <-chan time.Time, process *childProcess) error {
	select {
	case <-process.done:
		return process.exitedError()
	case <-tick:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("timed out waiting for whisper-server health: %w", ctx.Err())
	}
}

func (p *childProcess) exitedError() error {
	select {
	case <-p.done:
		if p.waitErr == nil {
			return errors.New("helper exited before it became ready")
		}

		return fmt.Errorf("helper exited before it became ready: %w", p.waitErr)
	default:
		return nil
	}
}

func loopbackHealthURL(base string) (string, error) {
	return loopbackWhisperEndpoint(base, "/health")
}

func loopbackWhisperEndpoint(base, endpoint string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse whisper-server URL: %w", err)
	}
	if !isPlainLoopbackURL(parsed) {
		return "", errors.New("whisper-server URL must be a plain HTTP 127.0.0.1 endpoint")
	}
	port, err := strconv.ParseUint(parsed.Port(), 10, 16)
	if err != nil {
		return "", fmt.Errorf("invalid whisper-server port: %w", err)
	}
	if port == 0 {
		return "", errors.New("whisper-server port must be positive")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + endpoint

	return parsed.String(), nil
}

func isPlainLoopbackURL(parsed *url.URL) bool {
	return parsed.Scheme == "http" && parsed.Hostname() == whisperLoopbackHost && parsed.Port() != "" &&
		parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && parsed.Opaque == "" &&
		parsed.RawPath == "" && !parsed.ForceQuery
}

func validateWhisperHealth(resp *http.Response) (bool, error) {
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, whisperHealthMaxBytes+1))
	closeErr := resp.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return false, fmt.Errorf("read whisper-server health: %w", err)
	}
	if len(raw) > whisperHealthMaxBytes {
		return false, fmt.Errorf("whisper-server health exceeds %d bytes", whisperHealthMaxBytes)
	}
	if err := validateWhisperHealthHeaders(resp.Header); err != nil {
		return false, err
	}

	return classifyWhisperHealth(resp.StatusCode, string(bytes.TrimSpace(raw)))
}

func validateWhisperHealthHeaders(header http.Header) error {
	if err := validateWhisperServerHeader(header); err != nil {
		return err
	}
	if values := header.Values("Content-Type"); len(values) != 1 || values[0] != contentTypeJSON {
		return errors.New("whisper-server health has an unexpected Content-Type header")
	}

	return nil
}

func validateWhisperServerHeader(header http.Header) error {
	if values := header.Values("Server"); len(values) != 1 || values[0] != whisperServerName {
		return errors.New("whisper-server response has an unexpected Server header")
	}

	return nil
}

func classifyWhisperHealth(status int, body string) (bool, error) {
	switch {
	case status == http.StatusOK && body == whisperHealthReady:
		return true, nil
	case status == http.StatusServiceUnavailable && body == whisperHealthLoading:
		return false, nil
	default:
		return false, fmt.Errorf("unexpected whisper-server health response: status=%d", status)
	}
}
