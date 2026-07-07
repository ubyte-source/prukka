// Package cartesia implements core.TTS over Cartesia's cloud voice API:
// each speaker is cloned once from a reference clip, then every take is
// synthesized in that voice. Cloning a real voice requires consent.
package cartesia

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/media/wav"
	"github.com/ubyte-source/prukka/internal/providers/helpers/rest"
)

// apiVersion pins the Cartesia-Version the request bodies below are written
// against; the platform dates every breaking change.
const apiVersion = "2026-03-01"

// referenceRate is the sample rate of the reference the engine captures
// (mono at the internal reference rate).
const referenceRate = 16000

// outputRate is Cartesia's pcm_s16le output rate, and the fallback when the
// config leaves the rate unset.
const outputRate = 24000

// Config carries the resolved Cartesia settings. The key is already resolved
// from its keychain reference by the caller.
type Config struct {
	BaseURL string
	Key     string
	Model   string
	Timeout time.Duration
	Rate    int
}

// Client synthesizes cloned-voice audio, caching one clone per speaker for
// the lane's lifetime.
type Client struct {
	httpc  *http.Client
	clones map[string]string
	cfg    Config
	mu     sync.Mutex
}

// New wires a client from the resolved config.
func New(cfg *Config) *Client {
	return &Client{
		httpc:  &http.Client{Timeout: cfg.Timeout},
		cfg:    *cfg,
		clones: make(map[string]string),
	}
}

// Compile-time port check.
var _ core.TTS = (*Client)(nil)

// Speak implements core.TTS: it resolves the speaker's cloned voice, then
// synthesizes the translated take in it.
func (c *Client) Speak(ctx context.Context, text string, to core.Lang, v core.Voice) (core.PCM, error) {
	voiceID, err := c.voiceID(ctx, to, v)
	if err != nil {
		return core.PCM{}, err
	}

	raw, err := c.synthesize(ctx, text, to, voiceID)
	if err != nil {
		return core.PCM{}, err
	}

	samples := make([]int16, len(raw)/2)
	if _, decErr := pipeline.DecodeS16LE(samples, raw); decErr != nil {
		return core.PCM{}, decErr
	}

	rate := c.cfg.Rate
	if rate <= 0 {
		rate = outputRate
	}

	return core.PCM{Data: samples, Rate: rate, Ch: 1}, nil
}

// voiceID resolves the voice for a speaker: cached clone, fresh clone, or
// the caller's id verbatim when no reference is present.
func (c *Client) voiceID(ctx context.Context, to core.Lang, v core.Voice) (string, error) {
	if len(v.Ref) == 0 {
		return v.ID, nil
	}

	c.mu.Lock()
	cached, ok := c.clones[v.ID]
	c.mu.Unlock()

	if ok {
		return cached, nil
	}

	id, err := c.clone(ctx, v.ID, to, v.Ref)
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	c.clones[v.ID] = id
	c.mu.Unlock()

	return id, nil
}

// cloneResponse carries the created voice id.
type cloneResponse struct {
	ID string `json:"id"`
}

// clone creates a Cartesia voice from the reference clip; clones persist
// account-side, named "prukka-…" so operators can bulk-clean them.
func (c *Client) clone(ctx context.Context, name string, to core.Lang, ref []int16) (string, error) {
	audio, err := wav.Encode(ref, referenceRate, 1)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer

	mw := multipart.NewWriter(&buf)

	clip, err := mw.CreateFormFile("clip", "reference.wav")
	if err != nil {
		return "", fmt.Errorf("build clone form: %w", err)
	}

	if _, writeErr := clip.Write(audio); writeErr != nil {
		return "", fmt.Errorf("build clone form: %w", writeErr)
	}

	if nameErr := multipartField(mw, "name", "prukka-"+name); nameErr != nil {
		return "", nameErr
	}

	if langErr := multipartField(mw, "language", baseTag(to)); langErr != nil {
		return "", langErr
	}

	if closeErr := mw.Close(); closeErr != nil {
		return "", fmt.Errorf("build clone form: %w", closeErr)
	}

	req, err := c.newRequest(ctx, "/voices/clone", mw.FormDataContentType(), &buf)
	if err != nil {
		return "", err
	}

	var resp cloneResponse
	if doErr := c.roundtrip(req, "/voices/clone", rest.JSONInto(&resp)); doErr != nil {
		return "", doErr
	}

	if resp.ID == "" {
		return "", fmt.Errorf("%w: clone reply carried no voice id", core.ErrTransient)
	}

	return resp.ID, nil
}

// voiceRef selects an existing Cartesia voice by id.
type voiceRef struct {
	Mode string `json:"mode"`
	ID   string `json:"id"`
}

// outputFormat asks for raw little-endian s16 PCM, the form the engine mixes.
type outputFormat struct {
	Container  string `json:"container"`
	Encoding   string `json:"encoding"`
	SampleRate int    `json:"sample_rate"`
}

// speakRequest is the /tts/bytes body.
type speakRequest struct {
	ModelID      string       `json:"model_id"`
	Transcript   string       `json:"transcript"`
	Language     string       `json:"language,omitempty"`
	Voice        voiceRef     `json:"voice"`
	OutputFormat outputFormat `json:"output_format"`
}

// synthesize reads the translated text in the cloned voice, returning raw
// s16 PCM bytes.
func (c *Client) synthesize(ctx context.Context, text string, to core.Lang, voiceID string) ([]byte, error) {
	rate := c.cfg.Rate
	if rate <= 0 {
		rate = outputRate
	}

	body, err := json.Marshal(&speakRequest{
		ModelID:      c.cfg.Model,
		Transcript:   text,
		Language:     baseTag(to),
		Voice:        voiceRef{Mode: "id", ID: voiceID},
		OutputFormat: outputFormat{Container: "raw", Encoding: "pcm_s16le", SampleRate: rate},
	})
	if err != nil {
		return nil, fmt.Errorf("encode tts request: %w", err)
	}

	req, err := c.newRequest(ctx, "/tts/bytes", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	var raw []byte
	if doErr := c.roundtrip(req, "/tts/bytes", rest.ReadAll(&raw)); doErr != nil {
		return nil, doErr
	}

	return raw, nil
}

// roundtrip posts one request and hands the reply to decode; requests are
// built in-package so no exported boundary carries a caller URL.
func (c *Client) roundtrip(req *http.Request, path string, decode func(io.Reader) error) (err error) {
	resp, doErr := c.httpc.Do(req)
	if doErr != nil {
		return rest.TransportError(path, doErr)
	}

	defer func() { err = errors.Join(err, resp.Body.Close()) }()

	if resp.StatusCode != http.StatusOK {
		return rest.StatusError(path, resp)
	}

	return decode(resp.Body)
}

// newRequest builds an authenticated, version-pinned Cartesia request.
func (c *Client) newRequest(ctx context.Context, path, contentType string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("build %s request: %w", path, err)
	}

	req.Header.Set("Authorization", "Bearer "+c.cfg.Key)
	req.Header.Set("Cartesia-Version", apiVersion)
	req.Header.Set("Content-Type", contentType)

	return req, nil
}

// baseTag reduces a BCP-47 tag to the ISO 639-1 base Cartesia expects.
func baseTag(l core.Lang) string {
	base, _, _ := strings.Cut(string(l), "-")

	return base
}

// multipartField appends one text field, wrapping any writer error.
func multipartField(mw *multipart.Writer, name, value string) error {
	if err := mw.WriteField(name, value); err != nil {
		return fmt.Errorf("build clone form: %w", err)
	}

	return nil
}
