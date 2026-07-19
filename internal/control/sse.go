package control

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"time"

	"github.com/ubyte-source/prukka/internal/core/session"
)

// sseHeartbeat keeps proxies from idling out quiet event streams.
const sseHeartbeat = 15 * time.Second

// wireSession is the JSON shape shared by SSE snapshots and events,
// matching the gateway's camelCase for v1.Session.
type wireSession struct {
	Flags        map[string]string `json:"flags,omitempty"`
	Slug         string            `json:"slug"`
	Profile      string            `json:"profile"`
	SourceLabel  string            `json:"sourceLabel"`
	Status       string            `json:"status"`
	Error        string            `json:"error,omitempty"`
	Langs        []string          `json:"langs"`
	DubbedLangs  []string          `json:"effectiveDubbedLangs"`
	DelaySeconds float64           `json:"delaySeconds"`
}

// toWire projects a stored session onto its JSON shape.
func toWire(s *session.Session, dubbed DubbedLanguagesFunc) wireSession {
	langs := langsToStrings(s.Langs)

	runtime := s.Runtime()

	return wireSession{
		Slug:         s.Slug,
		Profile:      string(s.Profile),
		SourceLabel:  PublicSourceLabel(s.Source.URL),
		Status:       string(runtime.State),
		Error:        runtime.Error,
		Langs:        langs,
		DubbedLangs:  projectedDubbedLanguages(dubbed, s),
		Flags:        maps.Clone(s.Flags),
		DelaySeconds: s.Delay.Seconds(),
	}
}

// sseHandler bridges store and engine events onto Server-Sent Events for
// the dashboard: a full snapshot first, then live events.
func sseHandler(
	store *session.Store, log *slog.Logger, dubbed DubbedLanguagesFunc, engine *Engine, token string,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveSSE(w, r, store, log, dubbed, engine, token)
	})
}

// serveSSE runs one SSE connection until the client goes away.
func serveSSE(
	w http.ResponseWriter, r *http.Request, store *session.Store, log *slog.Logger,
	dubbed DubbedLanguagesFunc, engine *Engine, token string,
) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)

		return
	}

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-store")

	// Subscribing before the snapshot means no event between the two can be
	// missed; a duplicate render is harmless, a gap is not. Session snapshots
	// stay open so the pre-setup dashboard renders; the engine sub-stream
	// carries the same install/provider detail the token-gated /api/v1/engine
	// read guards, so it is only wired for a client that proves the token. A
	// nil channel is a permanent no-op in streamSSE's select.
	events := store.Subscribe(r.Context())
	var engineEvents <-chan wireEngineEvent
	if engineStreamAuthorized(r, token) {
		engineEvents = engine.Subscribe(r.Context())
	}

	if err := writeSnapshot(w, store, dubbed); err != nil {
		log.Debug("sse client left during snapshot", "err", err)

		return
	}

	flusher.Flush()
	streamSSE(w, flusher, events, engineEvents, store, log, dubbed)
}

// engineStreamAuthorized reports whether this SSE client may receive engine
// progress events. EventSource cannot send an Authorization header, so the
// dashboard passes the control token as a query parameter; the comparison is
// constant-time to match the REST guard.
func engineStreamAuthorized(r *http.Request, token string) bool {
	got := r.URL.Query().Get("token")

	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

// streamSSE forwards events and periodic authoritative snapshots until the
// subscription closes. Snapshots repair any events dropped by the bounded
// subscriber buffer while also keeping proxies from idling the stream out.
func streamSSE(
	w io.Writer, flusher http.Flusher, events <-chan session.Event, engineEvents <-chan wireEngineEvent,
	store *session.Store, log *slog.Logger, dubbed DubbedLanguagesFunc,
) {
	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()

	for {
		select {
		case e, open := <-events:
			if !open {
				return
			}

			if err := writeEvent(w, &e, dubbed); err != nil {
				log.Debug("sse client left", "err", err)

				return
			}

			flusher.Flush()
		case progress := <-engineEvents:
			if err := writeSSE(w, "engine", progress); err != nil {
				log.Debug("sse client left during engine event", "err", err)

				return
			}

			flusher.Flush()
		case <-heartbeat.C:
			if err := writeSnapshot(w, store, dubbed); err != nil {
				return
			}

			flusher.Flush()
		}
	}
}

// writeSnapshot sends the current session list as one snapshot event.
func writeSnapshot(w io.Writer, store *session.Store, dubbed DubbedLanguagesFunc) error {
	sessions := store.List()

	wire := make([]wireSession, len(sessions))
	for i := range sessions {
		wire[i] = toWire(&sessions[i], dubbed)
	}

	return writeSSE(w, "snapshot", wire)
}

// writeEvent sends one lifecycle event.
func writeEvent(w io.Writer, e *session.Event, dubbed DubbedLanguagesFunc) error {
	payload := struct {
		Type    string      `json:"type"`
		Session wireSession `json:"session"`
	}{Type: string(e.Type), Session: toWire(&e.Session, dubbed)}

	return writeSSE(w, "session", payload)
}

// writeSSE frames one payload in SSE wire format.
func writeSSE(w io.Writer, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s event: %w", event, err)
	}

	if _, writeErr := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); writeErr != nil {
		return fmt.Errorf("write %s event: %w", event, writeErr)
	}

	return nil
}
