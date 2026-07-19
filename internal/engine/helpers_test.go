package engine

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"
)

// newWhisperStub serves one fixed response under whisper.cpp's mandatory
// Server identity. An empty contentType leaves the header unset: legacy
// server builds answer without declaring one.
func newWhisperStub(t *testing.T, contentType, body string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Server", whisperServerName)
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write whisper stub response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	return server
}

func filledSamples(count int, value int16) []int16 {
	return slices.Repeat([]int16{value}, count)
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

type shortWriteCloser struct{}

func (shortWriteCloser) Write(data []byte) (int, error) { return max(0, len(data)-1), nil }
func (shortWriteCloser) Close() error                   { return nil }

type fakeTTSSynth struct {
	err  error
	text string
	pcm  []int16
	rate int
}

type blockingWriteCloser struct {
	closed chan struct{}
	once   sync.Once
}

func newBlockingWriteCloser() *blockingWriteCloser {
	return &blockingWriteCloser{closed: make(chan struct{})}
}

func (w *blockingWriteCloser) Write([]byte) (int, error) {
	<-w.closed

	return 0, os.ErrClosed
}

func (w *blockingWriteCloser) Close() error {
	w.once.Do(func() { close(w.closed) })

	return nil
}

func (s *fakeTTSSynth) synthesize(text string) (pcm []int16, rate int, err error) {
	s.text = text

	return s.pcm, s.rate, s.err
}
