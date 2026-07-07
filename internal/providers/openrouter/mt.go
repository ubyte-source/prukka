package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/providers/helpers/chat"
)

// usageRequest asks OpenRouter to include cost accounting in the reply.
type usageRequest struct {
	Include bool `json:"include"`
}

// chatRequest is the /chat/completions request body.
type chatRequest struct {
	Model       string         `json:"model"`
	Messages    []chat.Message `json:"messages"`
	Usage       usageRequest   `json:"usage"`
	Temperature float64        `json:"temperature"`
}

// chatResponse is the subset of the reply the engine consumes.
type chatResponse struct {
	Choices []struct {
		Message chat.Message `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     float64 `json:"prompt_tokens"`
		CompletionTokens float64 `json:"completion_tokens"`
		Cost             float64 `json:"cost"`
	} `json:"usage"`
}

// Translate implements core.MT over /chat/completions with the shared
// translation-only prompt.
func (s *SessionClient) Translate(ctx context.Context, t core.Transcript, to core.Lang, o core.MTOpts) (string, error) {
	req := &chatRequest{
		Model:       s.c.cfg.Models.MT,
		Messages:    chat.BuildMessages(&t, to, &o),
		Usage:       usageRequest{Include: true},
		Temperature: s.c.cfg.Models.Temperature,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("encode chat request: %w", err)
	}

	var resp chatResponse
	if doErr := s.c.do(ctx, "/chat/completions", "application/json", bytes.NewReader(body), &resp); doErr != nil {
		return "", doErr
	}

	if len(resp.Choices) == 0 {
		// An empty choice list is a routing hiccup, not a caller bug.
		return "", fmt.Errorf("%w: chat reply carried no choices", core.ErrTransient)
	}

	tokens := resp.Usage.PromptTokens + resp.Usage.CompletionTokens
	s.c.meter.Add(s.slug, "mt", tokens, resp.Usage.Cost*s.c.cfg.EURPerUSD)

	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}
