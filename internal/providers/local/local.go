// Package local implements the AI ports against OpenAI-compatible servers
// on the operator's machine (Ollama, whisper.cpp, LocalAI, LM Studio,
// vLLM); each stage carries its own base URL. Nothing leaves the host.
package local

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/providers/helpers/rest"
)

// Endpoint locates the per-stage servers; the standard OpenAI path is
// appended to each base URL.
type Endpoint struct {
	STT     string
	MT      string
	TTS     string
	Timeout time.Duration
}

// Models selects the per-stage models, the MT sampling temperature and the
// TTS voice, wire format and reply sample rate.
type Models struct {
	STT         string
	MT          string
	TTS         string
	Voice       string
	Format      string
	Temperature float64
	Rate        int
}

// Config carries the resolved settings, grouped by responsibility: where to
// call each stage and which model and parameters to call it with.
type Config struct {
	Endpoint Endpoint
	Models   Models
}

// Client talks to the operator's OpenAI-compatible servers; it satisfies
// core.STT, core.MT and core.TTS.
type Client struct {
	httpc *http.Client
	cfg   Config
}

// New wires a client from the resolved config. The local path is unmetered:
// no audio or cost ever leaves the host.
func New(cfg *Config) *Client {
	return &Client{
		httpc: &http.Client{Timeout: cfg.Endpoint.Timeout},
		cfg:   *cfg,
	}
}

// Compile-time port checks.
var (
	_ core.STT = (*Client)(nil)
	_ core.MT  = (*Client)(nil)
	_ core.TTS = (*Client)(nil)
)

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

// pcmDuration reports the play time of an utterance's audio.
func pcmDuration(p *core.PCM) time.Duration {
	if p.Rate <= 0 || p.Ch <= 0 {
		return 0
	}

	return time.Duration(len(p.Data)/p.Ch) * time.Second / time.Duration(p.Rate)
}
