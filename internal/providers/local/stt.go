package local

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/ubyte-source/prukka/internal/core"
	"github.com/ubyte-source/prukka/internal/core/lang"
	"github.com/ubyte-source/prukka/internal/media/wav"
	"github.com/ubyte-source/prukka/internal/providers/helpers/rest"
)

// transcriptionResponse is the subset of OpenAI's transcription reply the
// engine reads. Whisper-family servers fill language on response_format=json.
type transcriptionResponse struct {
	Text     string `json:"text"`
	Language string `json:"language"`
}

// Transcribe implements core.STT over /audio/transcriptions with the
// standard OpenAI multipart form.
func (c *Client) Transcribe(ctx context.Context, u *core.Utterance, hint core.Lang) (core.Transcript, error) {
	encoded, err := wav.Encode(u.Audio.Data, u.Audio.Rate, u.Audio.Ch)
	if err != nil {
		return core.Transcript{}, err
	}

	body, contentType, err := transcriptionForm(encoded, c.cfg.Models.STT, hint)
	if err != nil {
		return core.Transcript{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint.STT+"/audio/transcriptions", body)
	if err != nil {
		return core.Transcript{}, fmt.Errorf("build transcription request: %w", err)
	}

	req.Header.Set("Content-Type", contentType)

	var resp transcriptionResponse
	if doErr := c.roundtrip(req, "/audio/transcriptions", rest.JSONInto(&resp)); doErr != nil {
		return core.Transcript{}, doErr
	}

	dur := pcmDuration(&u.Audio)

	return core.Transcript{
		Text: strings.TrimSpace(resp.Text),
		Lang: detectedOrHint(resp.Language, hint),
		Span: [2]time.Duration{u.Audio.PTS, u.Audio.PTS + dur},
	}, nil
}

// transcriptionForm builds the multipart upload: the WAV file plus the model
// and, when pinned, the language.
func transcriptionForm(audio []byte, model string, hint core.Lang) (io.Reader, string, error) {
	var buf bytes.Buffer

	mw := multipart.NewWriter(&buf)

	file, err := mw.CreateFormFile("file", "audio.wav")
	if err != nil {
		return nil, "", fmt.Errorf("build transcription form: %w", err)
	}

	if _, err = file.Write(audio); err != nil {
		return nil, "", fmt.Errorf("build transcription form: %w", err)
	}

	fields := [][2]string{{"model", model}, {"response_format", "json"}}
	if hint != core.LangAuto {
		// Whisper-style models want the bare ISO 639-1 base, not a region.
		base, _, _ := strings.Cut(string(hint), "-")
		fields = append(fields, [2]string{"language", base})
	}

	for _, f := range fields {
		if err = mw.WriteField(f[0], f[1]); err != nil {
			return nil, "", fmt.Errorf("build transcription form: %w", err)
		}
	}

	if err = mw.Close(); err != nil {
		return nil, "", fmt.Errorf("build transcription form: %w", err)
	}

	return &buf, mw.FormDataContentType(), nil
}

// detectedOrHint validates a server-reported language, falling back to the
// caller's hint when the server reports none or an unknown tag.
func detectedOrHint(reported string, hint core.Lang) core.Lang {
	if parsed, err := lang.Parse(reported); err == nil && parsed != core.LangAuto {
		return parsed
	}

	return hint
}
