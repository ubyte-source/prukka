package local

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/providers/helpers/chat"
	"github.com/ubyte-source/prukka/internal/providers/helpers/rest"
)

// chatRequest is the /chat/completions body. Streaming is off: the engine
// wants the whole translation in one reply.
type chatRequest struct {
	Model       string         `json:"model"`
	Messages    []chat.Message `json:"messages"`
	Temperature float64        `json:"temperature"`
	Stream      bool           `json:"stream"`
}

// chatResponse is the subset of the reply the engine consumes.
type chatResponse struct {
	Choices []struct {
		Message chat.Message `json:"message"`
	} `json:"choices"`
}

// Translate implements core.MT over /chat/completions with the shared
// translation-only prompt.
func (c *Client) Translate(ctx context.Context, t core.Transcript, to core.Lang, o core.MTOpts) (string, error) {
	body, err := json.Marshal(&chatRequest{
		Model:       c.cfg.Models.MT,
		Messages:    chat.BuildMessages(&t, to, &o),
		Temperature: c.cfg.Models.Temperature,
	})
	if err != nil {
		return "", fmt.Errorf("encode chat request: %w", err)
	}

	url := c.cfg.Endpoint.MT + "/chat/completions"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build chat request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	var resp chatResponse
	if doErr := c.roundtrip(req, "/chat/completions", rest.JSONInto(&resp)); doErr != nil {
		return "", doErr
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("%w: chat reply carried no choices", core.ErrTransient)
	}

	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}
