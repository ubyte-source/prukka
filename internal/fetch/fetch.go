// Package fetch is the one hardened downloader behind every artifact prukka
// pulls from the network: a TLS 1.2 floor, bounded connect and header phases,
// capped response headers, https-only redirects, and a byte ceiling on every
// body. A URL here may carry a credential, so errors name it only through
// redact.URL.
package fetch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/ubyte-source/prukka/internal/redact"
)

const maxRedirects = 10

// requestCause drops the URL net/http stores on every *url.Error it returns:
// Do masks the password alone, leaving the username and the query intact, and
// NewRequest echoes the whole raw URL.
func requestCause(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}

	return err
}

// Client issues hardened GETs; use New to build one.
type Client struct {
	http *http.Client
}

// New wires a client over the hardened transport. It sets no whole-request
// deadline — a multi-gigabyte model on a slow link is legitimate — so a caller
// that needs one bounds its own ctx.
func New() *Client {
	return &Client{http: &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:    30 * time.Second,
			ResponseHeaderTimeout:  30 * time.Second,
			IdleConnTimeout:        90 * time.Second,
			ExpectContinueTimeout:  time.Second,
			MaxResponseHeaderBytes: 1 << 20,
			ForceAttemptHTTP2:      true,
		},
		CheckRedirect: httpsOnlyRedirect,
	}}
}

// Want states what a verified download must satisfy: SHA256 must match, Limit
// caps the body, and a positive Size pins the exact published length.
type Want struct {
	SHA256 string
	Size   int64
	Limit  int64
}

// Bytes reads one response fully into memory, refusing more than limit bytes.
func (c *Client) Bytes(ctx context.Context, rawURL string, limit int64) (data []byte, err error) {
	body, err := c.get(ctx, rawURL, limit)
	if err != nil {
		return nil, err
	}

	defer func() { err = errors.Join(err, body.Close()) }()

	var buf bytes.Buffer
	if _, copyErr := CopyBounded(&buf, body, limit); copyErr != nil {
		return nil, fmt.Errorf("fetch %s: %w", redact.URL(rawURL), copyErr)
	}

	return buf.Bytes(), nil
}

// Verified streams one response into w while hashing it, failing unless the
// body satisfies want. w sees unverified bytes as they arrive, so a caller
// stages them and publishes only after a nil return.
func (c *Client) Verified(ctx context.Context, rawURL string, w io.Writer, want Want) (err error) {
	limit := max(want.Limit, want.Size)
	if limit <= 0 {
		return fmt.Errorf("fetch %s: no size limit declared", redact.URL(rawURL))
	}

	body, err := c.get(ctx, rawURL, limit)
	if err != nil {
		return err
	}

	defer func() { err = errors.Join(err, body.Close()) }()

	hash := sha256.New()

	copied, copyErr := CopyBounded(io.MultiWriter(w, hash), body, limit)
	if copyErr != nil {
		return fmt.Errorf("fetch %s: %w", redact.URL(rawURL), copyErr)
	}
	if want.Size > 0 && copied != want.Size {
		return fmt.Errorf("fetch %s: got %d bytes, want %d", redact.URL(rawURL), copied, want.Size)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want.SHA256 {
		return fmt.Errorf("fetch %s: checksum mismatch — refusing the payload", redact.URL(rawURL))
	}

	return nil
}

// get issues one GET and returns the body of a 200 response whose declared
// length fits the limit. The caller closes the body.
func (c *Client) get(ctx context.Context, rawURL string, limit int64) (io.ReadCloser, error) {
	display := redact.URL(rawURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", display, requestCause(err))
	}

	reply, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", display, requestCause(err))
	}
	if reply.StatusCode != http.StatusOK {
		return nil, errors.Join(
			fmt.Errorf("fetch %s: http %d", display, reply.StatusCode), reply.Body.Close(),
		)
	}
	if reply.ContentLength > limit {
		return nil, errors.Join(
			fmt.Errorf("fetch %s: payload declares %d bytes, over the %d limit", display, reply.ContentLength, limit),
			reply.Body.Close(),
		)
	}

	return reply.Body, nil
}

func httpsOnlyRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return errors.New("too many download redirects")
	}
	if req.URL.Scheme != "https" {
		// (*url.URL).Redacted() masks the password but keeps the path and the
		// query, which is exactly what redact.URL exists to remove.
		return fmt.Errorf("refusing non-https redirect to %s", redact.URL(req.URL.String()))
	}

	return nil
}

// CopyBounded copies src into dst, observing at most limit+1 bytes so an
// exact-size payload is distinguishable from a truncated oversized one.
func CopyBounded(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	written, err := io.Copy(dst, io.LimitReader(src, limit+1))
	if err != nil {
		return written, err
	}
	if written > limit {
		return written, fmt.Errorf("payload exceeds %d bytes", limit)
	}

	return written, nil
}
