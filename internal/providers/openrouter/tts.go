package openrouter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/lang"
	"github.com/ubyte-source/prukka/internal/core/pipeline"
	"github.com/ubyte-source/prukka/internal/providers/helpers/chat"
	"github.com/ubyte-source/prukka/internal/providers/helpers/rest"
)

// ttsRate is the sample rate of the provider's pcm16 output.
const ttsRate = 24000

// defaultVoice is the last resort when the voice map has no entry.
const defaultVoice = "alloy"

// ssePrefix frames server-sent events on the streaming endpoint.
const ssePrefix = "data: "

// maxSSELine bounds one streamed event; audio deltas stay well under it.
const maxSSELine = 4 << 20

// ttsRequest is the audio-output chat request. The endpoint mandates
// streaming for audio; the reply arrives as SSE deltas.
type ttsRequest struct {
	Model      string         `json:"model"`
	Modalities []string       `json:"modalities"`
	Audio      ttsAudio       `json:"audio"`
	Messages   []chat.Message `json:"messages"`
	Usage      usageRequest   `json:"usage"`
	Stream     bool           `json:"stream"`
}

// ttsAudio selects voice and wire format.
type ttsAudio struct {
	Voice  string `json:"voice"`
	Format string `json:"format"`
}

// ttsDelta is the subset of one streamed chunk the engine consumes.
type ttsDelta struct {
	Usage *struct {
		Cost float64 `json:"cost"`
	} `json:"usage"`
	Choices []struct {
		Delta struct {
			Audio struct {
				Data string `json:"data"`
			} `json:"audio"`
		} `json:"delta"`
	} `json:"choices"`
}

// Speak implements core.TTS over streaming chat completions; the model is
// prompted as a dubbing actor or it would converse instead of reading.
func (s *SessionClient) Speak(ctx context.Context, text string, to core.Lang, v core.Voice) (core.PCM, error) {
	voice := v.ID
	if voice == "" {
		voice = defaultVoice
	}

	body, err := json.Marshal(&ttsRequest{
		Model:      s.c.cfg.Models.TTS,
		Modalities: []string{"text", "audio"},
		Audio:      ttsAudio{Voice: voice, Format: "pcm16"},
		Messages: []chat.Message{
			{Role: "system", Content: "You are a professional dubbing voice actor in a recording booth. " +
				"Your ONLY job: read the script between <script></script> tags aloud EXACTLY as written, " +
				"in " + lang.Describe(to) + ", with natural delivery. You never reply, never translate, " +
				"never add or omit a single word. Output nothing but the read."},
			{Role: "user", Content: "<script>" + text + "</script>"},
		},
		Usage:  usageRequest{Include: true},
		Stream: true,
	})
	if err != nil {
		return core.PCM{}, fmt.Errorf("encode tts request: %w", err)
	}

	raw, cost, speakErr := s.c.streamAudio(ctx, bytes.NewReader(body))
	if speakErr != nil {
		return core.PCM{}, speakErr
	}

	samples := make([]int16, len(raw)/2)
	if _, decodeErr := pipeline.DecodeS16LE(samples, raw); decodeErr != nil {
		return core.PCM{}, decodeErr
	}

	seconds := float64(len(samples)) / ttsRate
	s.c.meter.Add(s.slug, "tts", seconds, cost*s.c.cfg.EURPerUSD)

	return core.PCM{Data: samples, Rate: ttsRate, Ch: 1}, nil
}

// streamAudio drains the SSE reply, reassembling audio deltas and picking
// up the final usage cost.
func (c *Client) streamAudio(ctx context.Context, body *bytes.Reader) (raw []byte, cost float64, err error) {
	req, err := c.newRequest(ctx, "/chat/completions", "application/json", body)
	if err != nil {
		return nil, 0, err
	}

	resp, doErr := c.httpc.Do(req)
	if doErr != nil {
		return nil, 0, rest.TransportError("/chat/completions", doErr)
	}

	defer func() { err = errors.Join(err, resp.Body.Close()) }()

	if resp.StatusCode != http.StatusOK {
		return nil, 0, rest.StatusError("/chat/completions", resp)
	}

	return drainSSE(resp.Body)
}

// drainSSE reassembles audio deltas from the event stream.
func drainSSE(body io.Reader) (raw []byte, cost float64, err error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), maxSSELine)

	var audio bytes.Buffer

	for scanner.Scan() {
		line := strings.TrimPrefix(scanner.Text(), ssePrefix)
		if line == scanner.Text() || line == "[DONE]" {
			continue
		}

		chunkCost, chunkErr := appendDelta(&audio, line)
		if chunkErr != nil {
			return nil, 0, chunkErr
		}

		if chunkCost > 0 {
			cost = chunkCost
		}
	}

	if scanErr := scanner.Err(); scanErr != nil {
		return nil, 0, fmt.Errorf("%w: read tts stream: %w", core.ErrTransient, scanErr)
	}

	if audio.Len() == 0 {
		return nil, 0, fmt.Errorf("%w: tts stream carried no audio", core.ErrTransient)
	}

	return audio.Bytes(), cost, nil
}

// appendDelta decodes one streamed chunk into the audio buffer, returning
// any usage cost it carried.
func appendDelta(audio *bytes.Buffer, line string) (float64, error) {
	var delta ttsDelta
	if err := json.Unmarshal([]byte(line), &delta); err != nil {
		return 0, fmt.Errorf("decode tts chunk: %w", err)
	}

	for _, choice := range delta.Choices {
		if data := choice.Delta.Audio.Data; data != "" {
			decoded, err := base64.StdEncoding.DecodeString(data)
			if err != nil {
				return 0, fmt.Errorf("decode audio delta: %w", err)
			}

			audio.Write(decoded)
		}
	}

	if delta.Usage != nil {
		return delta.Usage.Cost, nil
	}

	return 0, nil
}
