package control

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/ubyte-source/prukka/internal/core/session"
)

// sseHeartbeat keeps proxies from idling out quiet event streams.
const sseHeartbeat = 15 * time.Second

// wireSession is the JSON shape shared by SSE snapshots and events,
// matching the gateway's camelCase for v1.Session.
type wireSession struct {
	Slug             string   `json:"slug"`
	Profile          string   `json:"profile"`
	SourceURL        string   `json:"sourceUrl"`
	Langs            []string `json:"langs"`
	BudgetEURPerHour float64  `json:"budgetEurPerHour"`
	DelaySeconds     float64  `json:"delaySeconds"`
}

// toWire projects a stored session onto its JSON shape.
func toWire(s *session.Session) wireSession {
	langs := make([]string, len(s.Langs))
	for i, l := range s.Langs {
		langs[i] = string(l)
	}

	return wireSession{
		Slug:             s.Slug,
		Profile:          string(s.Profile),
		SourceURL:        s.Source.URL,
		Langs:            langs,
		BudgetEURPerHour: s.BudgetEURPerHour,
		DelaySeconds:     s.Delay.Seconds(),
	}
}

// sseHandler bridges store events onto Server-Sent Events for the
// dashboard: a full snapshot first, then live events.
func sseHandler(store *session.Store, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveSSE(w, r, store, log)
	})
}

// serveSSE runs one SSE connection until the client goes away.
func serveSSE(w http.ResponseWriter, r *http.Request, store *session.Store, log *slog.Logger) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)

		return
	}

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-store")

	// Subscribing before the snapshot means no event between the two can be
	// missed; a duplicate render is harmless, a gap is not.
	events := store.Subscribe(r.Context())

	if err := writeSnapshot(w, store); err != nil {
		log.Debug("sse client left during snapshot", "err", err)

		return
	}

	flusher.Flush()
	streamSSE(w, flusher, events, log)
}

// streamSSE forwards events and heartbeats until the subscription closes.
func streamSSE(w io.Writer, flusher http.Flusher, events <-chan session.Event, log *slog.Logger) {
	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()

	for {
		select {
		case e, open := <-events:
			if !open {
				return
			}

			if err := writeEvent(w, &e); err != nil {
				log.Debug("sse client left", "err", err)

				return
			}

			flusher.Flush()
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}

			flusher.Flush()
		}
	}
}

// writeSnapshot sends the current session list as one snapshot event.
func writeSnapshot(w io.Writer, store *session.Store) error {
	sessions := store.List()

	wire := make([]wireSession, len(sessions))
	for i := range sessions {
		wire[i] = toWire(&sessions[i])
	}

	return writeSSE(w, "snapshot", wire)
}

// writeEvent sends one lifecycle event.
func writeEvent(w io.Writer, e *session.Event) error {
	payload := struct {
		Type    string      `json:"type"`
		Session wireSession `json:"session"`
	}{Type: string(e.Type), Session: toWire(&e.Session)}

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
