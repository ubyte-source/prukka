// Package fetch is the one hardened downloader behind every artifact prukka
// pulls from the network: a TLS 1.2 floor, bounded connect and header phases,
// capped response headers, https-only redirects, and a byte ceiling on every
// body. Artifacts that publish a SHA-256 are verified here, so no call site
// can install bytes it never checked.
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
	"time"
)

// maxRedirects bounds one download's redirect chain.
const maxRedirects = 10

// Client issues hardened GETs; use New to build one.
type Client struct {
	http *http.Client
}

// New wires a client over the hardened transport. It sets no whole-request
// deadline on purpose: a multi-gigabyte model on a slow link is legitimate,
// and a caller that needs one bounds its own ctx.
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

// Want states what a verified download must satisfy: its SHA-256 must equal
// SHA256, and it must not exceed Limit bytes. A positive Size pins the exact
// published length and caps the body on its own.
type Want struct {
	SHA256 string
	Size   int64
	Limit  int64
}

// Bytes reads one response fully into memory, refusing more than limit bytes.
func (c *Client) Bytes(ctx context.Context, url string, limit int64) (data []byte, err error) {
	body, err := c.get(ctx, url, limit)
	if err != nil {
		return nil, err
	}

	defer func() { err = errors.Join(err, body.Close()) }()

	var buf bytes.Buffer
	if _, copyErr := copyBounded(&buf, body, limit); copyErr != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, copyErr)
	}

	return buf.Bytes(), nil
}

// Verified streams one response into w while hashing it, and reports failure
// unless the body satisfies want. w sees the bytes as they arrive, so a caller
// stages them (a temp file, a buffer) and publishes only after a nil return.
func (c *Client) Verified(ctx context.Context, url string, w io.Writer, want Want) (err error) {
	limit := max(want.Limit, want.Size)
	if limit <= 0 {
		return fmt.Errorf("fetch %s: no size limit declared", url)
	}

	body, err := c.get(ctx, url, limit)
	if err != nil {
		return err
	}

	defer func() { err = errors.Join(err, body.Close()) }()

	hash := sha256.New()

	copied, copyErr := copyBounded(io.MultiWriter(w, hash), body, limit)
	if copyErr != nil {
		return fmt.Errorf("fetch %s: %w", url, copyErr)
	}
	if want.Size > 0 && copied != want.Size {
		return fmt.Errorf("fetch %s: got %d bytes, want %d", url, copied, want.Size)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want.SHA256 {
		return fmt.Errorf("fetch %s: checksum mismatch — refusing the payload", url)
	}

	return nil
}

// get issues one GET and returns the body of a 200 response whose declared
// length fits the limit. The caller closes the body.
func (c *Client) get(ctx context.Context, url string, limit int64) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", url, err)
	}

	reply, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	if reply.StatusCode != http.StatusOK {
		return nil, errors.Join(
			fmt.Errorf("fetch %s: http %d", url, reply.StatusCode), reply.Body.Close(),
		)
	}
	if reply.ContentLength > limit {
		return nil, errors.Join(
			fmt.Errorf("fetch %s: payload declares %d bytes, over the %d limit", url, reply.ContentLength, limit),
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
		return fmt.Errorf("refusing non-https redirect to %s", req.URL.Redacted())
	}

	return nil
}

// copyBounded observes at most limit+1 bytes so an exact-size payload is
// distinguishable from a truncated oversized one.
func copyBounded(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	written, err := io.Copy(dst, io.LimitReader(src, limit+1))
	if err != nil {
		return written, err
	}
	if written > limit {
		return written, fmt.Errorf("payload exceeds %d bytes", limit)
	}

	return written, nil
}
