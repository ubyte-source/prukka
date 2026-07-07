package openrouter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/lang"
	"github.com/ubyte-source/prukka/internal/media/wav"
)

// transcriptionRequestBody is OpenRouter's transcription request: base64
// audio in input_audio — the endpoint rejects multipart.
type transcriptionRequestBody struct {
	Model      string     `json:"model"`
	Language   string     `json:"language,omitempty"`
	InputAudio inputAudio `json:"input_audio"`
}

// inputAudio carries the encoded chunk.
type inputAudio struct {
	Data   string `json:"data"`
	Format string `json:"format"`
}

// transcriptionResponse is the transcription reply; usage carries the
// billable seconds and the real USD cost.
type transcriptionResponse struct {
	Text  string `json:"text"`
	Usage struct {
		Seconds float64 `json:"seconds"`
		Cost    float64 `json:"cost"`
	} `json:"usage"`
}

// Transcribe implements core.STT over /audio/transcriptions; the reply's
// cost lands in the meter.
func (s *SessionClient) Transcribe(ctx context.Context, u *core.Utterance, hint core.Lang) (core.Transcript, error) {
	body, err := transcriptionRequest(&u.Audio, s.c.cfg.Models.STT, hint)
	if err != nil {
		return core.Transcript{}, err
	}

	var resp transcriptionResponse
	if doErr := s.c.do(ctx, "/audio/transcriptions", "application/json", body, &resp); doErr != nil {
		return core.Transcript{}, doErr
	}

	dur := time.Duration(len(u.Audio.Data)/u.Audio.Ch) * time.Second / time.Duration(u.Audio.Rate)

	seconds := resp.Usage.Seconds
	if seconds == 0 {
		seconds = dur.Seconds()
	}

	s.c.meter.Add(s.slug, "stt", seconds, resp.Usage.Cost*s.c.cfg.EURPerUSD)

	return core.Transcript{
		Text: strings.TrimSpace(resp.Text),
		Lang: detectedOrHint("", hint),
		Span: [2]time.Duration{u.Audio.PTS, u.Audio.PTS + dur},
	}, nil
}

// transcriptionRequest builds the JSON body for one utterance.
func transcriptionRequest(audio *core.PCM, model string, hint core.Lang) (io.Reader, error) {
	encoded, err := wav.Encode(audio.Data, audio.Rate, audio.Ch)
	if err != nil {
		return nil, err
	}

	req := transcriptionRequestBody{
		Model:      model,
		InputAudio: inputAudio{Data: base64.StdEncoding.EncodeToString(encoded), Format: "wav"},
	}

	if hint != core.LangAuto {
		// Whisper-style models want the bare ISO 639-1 base, not a region.
		req.Language, _, _ = strings.Cut(string(hint), "-")
	}

	body, marshalErr := json.Marshal(req)
	if marshalErr != nil {
		return nil, fmt.Errorf("encode transcription request: %w", marshalErr)
	}

	return bytes.NewReader(body), nil
}

// detectedOrHint validates a provider-reported language, falling back to
// the caller's hint.
func detectedOrHint(reported string, hint core.Lang) core.Lang {
	if parsed, err := lang.Parse(reported); err == nil && parsed != core.LangAuto {
		return parsed
	}

	return hint
}
