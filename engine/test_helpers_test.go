package main

import (
	"io"
	"os"
	"sync"
)

func filledSamples(count int, value int16) []int16 {
	out := make([]int16, count)
	for i := range out {
		out[i] = value
	}

	return out
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
