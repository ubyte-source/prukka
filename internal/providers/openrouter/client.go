// Package openrouter implements the AI ports over the OpenRouter API;
// models and parameters come from config, nothing is hardcoded.
package openrouter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/providers/helpers/rest"
)

// Endpoint locates the API; the key arrives already resolved, this package
// never sees keychain references.
type Endpoint struct {
	BaseURL string
	Key     string
	Timeout time.Duration
}

// Models selects the per-stage models and the MT sampling temperature.
type Models struct {
	STT         string
	MT          string
	TTS         string
	Temperature float64
}

// Config carries the resolved provider settings, grouped by responsibility:
// where to call, which models to call, and how usage cost converts to euros.
type Config struct {
	Endpoint  Endpoint
	Models    Models
	EURPerUSD float64
}

// Client is the shared OpenRouter transport: one pooled HTTP client per
// daemon. Obtain port implementations with ForSession.
type Client struct {
	httpc *http.Client
	meter core.Meter
	cfg   Config
}

// New wires the client. Every call reports usage to the meter.
func New(cfg *Config, m core.Meter) *Client {
	return &Client{
		httpc: &http.Client{Timeout: cfg.Endpoint.Timeout},
		meter: m,
		cfg:   *cfg,
	}
}

// ForSession returns the port implementations whose usage is billed to the
// given session slug — cost attribution stays out of the port signatures.
func (c *Client) ForSession(slug string) *SessionClient {
	return &SessionClient{c: c, slug: slug}
}

// SessionClient tags every provider call with its owning session. It
// implements core.STT and core.MT.
type SessionClient struct {
	c    *Client
	slug string
}

// Compile-time port checks.
var (
	_ core.STT = (*SessionClient)(nil)
	_ core.MT  = (*SessionClient)(nil)
	_ core.TTS = (*SessionClient)(nil)
)

// newRequest builds an authenticated API request.
func (c *Client) newRequest(ctx context.Context, path, contentType string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint.BaseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("build %s request: %w", path, err)
	}

	req.Header.Set("Authorization", "Bearer "+c.cfg.Endpoint.Key)
	req.Header.Set("Content-Type", contentType)

	return req, nil
}

// do posts one request and decodes the JSON reply; requests are built
// in-package so no exported boundary carries a caller URL.
func (c *Client) do(ctx context.Context, path, contentType string, body io.Reader, out any) (err error) {
	req, err := c.newRequest(ctx, path, contentType, body)
	if err != nil {
		return err
	}

	resp, doErr := c.httpc.Do(req)
	if doErr != nil {
		return rest.TransportError(path, doErr)
	}

	defer func() { err = errors.Join(err, resp.Body.Close()) }()

	if resp.StatusCode != http.StatusOK {
		return rest.StatusError(path, resp)
	}

	return rest.JSONInto(out)(resp.Body)
}
