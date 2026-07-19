package speechengine

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestParseMTOptions(t *testing.T) {
	t.Parallel()

	from, to, parseErr := parseMTOptions([]string{"--from=IT", "--to", "en-US"})
	if parseErr != nil || from != "it" || to != "en-us" {
		t.Fatalf("parseMTOptions = (%q, %q, %v)", from, to, parseErr)
	}
	if _, _, invalidErr := parseMTOptions([]string{"--from", "../../models", "--to", "en"}); invalidErr == nil {
		t.Fatal("parseMTOptions accepted a path as a language")
	}
}

// The daemon's lockstep client blocks on each reply line, so the exact framing
// — one compact JSON object, one trailing newline, no buffering — is the
// contract this loop must keep.
func TestStreamMTFramesEachReplyAsOneJSONLine(t *testing.T) {
	t.Parallel()

	var got []string
	var out bytes.Buffer
	err := streamMT(
		strings.NewReader(`{"text":"ciao"}`+"\n"+`{"text":"mondo"}`+"\n"), &out,
		func(text string) (string, error) {
			got = append(got, text)

			return "mt:" + text, nil
		},
	)
	if err != nil {
		t.Fatalf("streamMT: %v", err)
	}
	if len(got) != 2 || got[0] != "ciao" || got[1] != "mondo" {
		t.Fatalf("translated requests = %q, want ciao then mondo", got)
	}
	if want := "{\"text\":\"mt:ciao\"}\n{\"text\":\"mt:mondo\"}\n"; out.String() != want {
		t.Fatalf("replies = %q, want %q", out.String(), want)
	}
}

func TestStreamMTStopsOnDecodeAndTranslateFailures(t *testing.T) {
	t.Parallel()

	echo := func(text string) (string, error) { return text, nil }

	err := streamMT(strings.NewReader("not json\n"), io.Discard, echo)
	if err == nil || !strings.Contains(err.Error(), "mt: decode request") {
		t.Fatalf("malformed request error = %v, want decode failure", err)
	}

	var out bytes.Buffer
	wantErr := errors.New("model exploded")
	err = streamMT(strings.NewReader(`{"text":"ciao"}`+"\n"), &out, func(string) (string, error) {
		return "", wantErr
	})
	if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "mt: translate") {
		t.Fatalf("translate error = %v, want wrapped %v", err, wantErr)
	}
	if out.Len() != 0 {
		t.Fatalf("failed translation still wrote %q", out.String())
	}
}

// The scanner's error tail must surface: an oversized request line is a
// protocol failure, not a silent clean EOF.
func TestStreamMTReportsOversizedRequestLines(t *testing.T) {
	t.Parallel()

	oversized := `{"text":"` + strings.Repeat("x", 1<<20) + `"}` + "\n"
	err := streamMT(strings.NewReader(oversized), io.Discard,
		func(text string) (string, error) { return text, nil })
	if !errors.Is(err, bufio.ErrTooLong) {
		t.Fatalf("oversized line error = %v, want bufio.ErrTooLong", err)
	}
}

func TestMTProcTranslateProtocol(t *testing.T) {
	t.Parallel()

	reader, writer := io.Pipe()
	requests := &bytes.Buffer{}
	proc := &mtProc{stdioProc: &stdioProc{stdin: nopWriteCloser{Writer: requests}}, out: bufio.NewScanner(reader)}

	go func() {
		if _, err := io.WriteString(writer, "hello world\n"); err != nil {
			t.Errorf("write fake translation: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Errorf("close fake translation: %v", err)
		}
	}()

	got, err := proc.translate("  ciao\r\nmondo  ")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if got != "hello world" {
		t.Fatalf("translation = %q", got)
	}
	if requests.String() != "  ciao  mondo  \n" {
		t.Fatalf("helper request = %q", requests.String())
	}
}

func TestMTProcSkipsBlankInput(t *testing.T) {
	t.Parallel()

	requests := &bytes.Buffer{}
	proc := &mtProc{stdioProc: &stdioProc{stdin: nopWriteCloser{Writer: requests}}}
	got, err := proc.translate(" \n\t")
	if err != nil || got != "" || requests.Len() != 0 {
		t.Fatalf("blank translate = %q, %v, writes=%d", got, err, requests.Len())
	}
}

func TestMTProcBoundsWriteAndResponse(t *testing.T) {
	t.Parallel()

	t.Run("blocked write", func(t *testing.T) {
		t.Parallel()

		writer := newBlockingWriteCloser()
		proc := &mtProc{stdioProc: &stdioProc{stdin: writer}}
		start := time.Now()
		_, err := proc.translateWithin("hello", 20*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "write timed out") {
			t.Fatalf("translate error = %v, want write timeout", err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("blocked write took %v to abort", elapsed)
		}
	})

	t.Run("missing response", func(t *testing.T) {
		t.Parallel()

		reader, writer := io.Pipe()
		t.Cleanup(func() {
			if closeErr := writer.Close(); closeErr != nil {
				t.Errorf("close response writer: %v", closeErr)
			}
		})
		proc := &mtProc{
			stdioProc: &stdioProc{stdin: nopWriteCloser{Writer: io.Discard}},
			stdout:    reader,
			out:       bufio.NewScanner(reader),
		}
		start := time.Now()
		_, err := proc.translateWithin("hello", 20*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "response timed out") {
			t.Fatalf("translate error = %v, want response timeout", err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("missing response took %v to abort", elapsed)
		}
	})

	t.Run("short write", func(t *testing.T) {
		t.Parallel()

		proc := &mtProc{stdioProc: &stdioProc{stdin: shortWriteCloser{}}}
		_, err := proc.translateWithin("hello", time.Second)
		if !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("translate error = %v, want short write", err)
		}
	})
}
