package local

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/providers/helpers/rest"
)

// speechRate is the sample rate OpenAI's pcm response format emits, and the
// fallback when the config leaves the rate unset.
const speechRate = 24000

// speechRequest is the /audio/speech body; isochrony happens downstream
// via atempo.
type speechRequest struct {
	Model          string `json:"model"`
	Input          string `json:"input"`
	Voice          string `json:"voice"`
	ResponseFormat string `json:"response_format"`
}

// Speak implements core.TTS over /audio/speech: raw s16le PCM decoded at
// the configured rate; this backend does not clone.
func (c *Client) Speak(ctx context.Context, text string, _ core.Lang, v core.Voice) (core.PCM, error) {
	voice := v.ID
	if voice == "" {
		voice = c.cfg.Models.Voice
	}

	body, err := json.Marshal(&speechRequest{
		Model:          c.cfg.Models.TTS,
		Input:          text,
		Voice:          voice,
		ResponseFormat: c.cfg.Models.Format,
	})
	if err != nil {
		return core.PCM{}, fmt.Errorf("encode speech request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint.TTS+"/audio/speech", bytes.NewReader(body))
	if err != nil {
		return core.PCM{}, fmt.Errorf("build speech request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	var raw []byte
	if doErr := c.roundtrip(req, "/audio/speech", rest.ReadAll(&raw)); doErr != nil {
		return core.PCM{}, doErr
	}

	samples := make([]int16, len(raw)/2)
	if _, decErr := pipeline.DecodeS16LE(samples, raw); decErr != nil {
		return core.PCM{}, decErr
	}

	rate := c.cfg.Models.Rate
	if rate <= 0 {
		rate = speechRate
	}

	return core.PCM{Data: samples, Rate: rate, Ch: 1}, nil
}
