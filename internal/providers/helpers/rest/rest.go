// Package rest is the reply plumbing shared by every HTTP provider;
// requests stay in-package so no boundary carries a caller URL (G107).
package rest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ubyte-source/prukka/internal/core"
)

// maxErrorBody bounds how much of an error reply lands in the message.
const maxErrorBody = 512

// errorEnvelope is the {"error":{"message":…}} failure shape shared by
// OpenAI-compatible servers, OpenRouter and Cartesia.
type errorEnvelope struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// JSONInto decodes a JSON reply body into out.
func JSONInto(out any) func(io.Reader) error {
	return func(body io.Reader) error {
		if err := json.NewDecoder(body).Decode(out); err != nil {
			return fmt.Errorf("decode reply: %w", err)
		}

		return nil
	}
}

// ReadAll drains a raw (non-JSON) reply body into dst, marking a read
// failure transient: the status line already promised a payload.
func ReadAll(dst *[]byte) func(io.Reader) error {
	return func(body io.Reader) error {
		b, err := io.ReadAll(body)
		if err != nil {
			return fmt.Errorf("%w: read reply body: %w", core.ErrTransient, err)
		}

		*dst = b

		return nil
	}
}

// TransportError wraps a network-level failure from posting path as
// transient: connection and TLS hiccups are worth a retry.
func TransportError(path string, err error) error {
	return fmt.Errorf("%w: %s: %w", core.ErrTransient, path, err)
}

// StatusError classifies a non-200 reply (429/5xx transient), carrying the
// body's error message so the operator never guesses.
func StatusError(path string, resp *http.Response) error {
	detail := ""

	if body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody)); err == nil && len(body) > 0 {
		detail = ": " + strings.TrimSpace(string(body))

		var envelope errorEnvelope
		if jsonErr := json.Unmarshal(body, &envelope); jsonErr == nil && envelope.Error.Message != "" {
			detail = ": " + envelope.Error.Message
		}
	}

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError {
		return fmt.Errorf("%w: %s: http %d%s", core.ErrTransient, path, resp.StatusCode, detail)
	}

	return fmt.Errorf("%s: http %d%s", path, resp.StatusCode, detail)
}
