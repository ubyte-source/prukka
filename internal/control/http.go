package control

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"

	"github.com/ubyte-source/prukka/internal/core/session"
	"github.com/ubyte-source/prukka/internal/webui"

	v1 "github.com/ubyte-source/prukka/internal/gen/prukka/v1"
)

// controlBodyMaxBytes bounds every REST mutation/configuration document. The
// control API has no upload endpoint; media enters through configured sources.
const controlBodyMaxBytes = 1 << 20

// CaptionDocs serves rendered caption documents for the data plane
// (consumer-side port; the vtt registry implements it).
type CaptionDocs interface {
	Document(session, lang string) ([]byte, bool)
}

// AudioStreams serves live dubbed audio; ServeTS blocks until the client
// leaves, false means the pair is unknown or dubbing is off.
type AudioStreams interface {
	ServeTS(ctx context.Context, w io.Writer, session, lang string) bool
}

// MediaTree serves one session's HLS output; the store rebuilds
// every path from its own components, so request data never names a file.
type MediaTree interface {
	MasterPlaylist(slug string) ([]byte, bool)
	Open(slug, rest string) (io.ReadSeekCloser, bool)
}

// httpDeps groups the HTTP surface's collaborators.
type httpDeps struct {
	store      *session.Store
	data       DataPlane
	metrics    http.Handler
	dubbed     DubbedLanguagesFunc
	ipcTLS     *tls.Config
	log        *slog.Logger
	token      string
	ipcPath    string
	corsOrigin string
	bind       string
}

// newHTTPHandler assembles the HTTP surface: dashboard, REST gateway, SSE,
// data plane, metrics and health.
func newHTTPHandler(ctx context.Context, d *httpDeps) (http.Handler, error) {
	gwConn, err := dialGRPC(d.ipcPath, d.token, d.ipcTLS)
	if err != nil {
		return nil, err
	}

	// The connection lives as long as the server context; this goroutine
	// owns its close.
	go func() {
		<-ctx.Done()

		if closeErr := gwConn.Close(); closeErr != nil {
			d.log.Warn("closing gateway connection", "err", closeErr)
		}
	}()

	gw := runtime.NewServeMux()
	if regErr := v1.RegisterControlHandler(ctx, gw, gwConn); regErr != nil {
		return nil, errors.Join(fmt.Errorf("register gateway: %w", regErr), gwConn.Close())
	}

	mux := http.NewServeMux()
	mux.Handle("/", rootHandler(d.data))
	mux.Handle("/ui/", http.StripPrefix("/ui", webui.Handler()))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	if d.metrics != nil {
		mux.Handle("/metrics", d.metrics)
	}

	mux.Handle("/api/v1/events", sseHandler(d.store, d.log, d.dubbed))
	mux.Handle("/api/v1/", controlAPIHandler(d.token, gw))

	return securityHeaders(hostGuard(d.bind, corsMiddleware(d.corsOrigin, mux))), nil
}

// securityHeaders constrains the browser surface without assuming TLS: the
// daemon normally serves an operator's loopback interface over plain HTTP.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers := w.Header()
		headers.Set("Content-Security-Policy",
			"default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; "+
				"form-action 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'")
		headers.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		headers.Set("Referrer-Policy", "no-referrer")
		headers.Set("X-Content-Type-Options", "nosniff")
		headers.Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			headers.Set("Cache-Control", "no-store")
		}

		next.ServeHTTP(w, r)
	})
}

// hostGuard refuses foreign Host headers on loopback binds (DNS-rebinding
// defense); a non-loopback bind is an operator choice and passes through.
func hostGuard(bind string, next http.Handler) http.Handler {
	if !loopbackName(hostOf(bind)) {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackName(hostOf(r.Host)) {
			http.Error(w, "unrecognized Host", http.StatusMisdirectedRequest)

			return
		}

		next.ServeHTTP(w, r)
	})
}

// hostOf strips the port from a host:port pair, tolerating a bare host.
func hostOf(hostport string) string {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return hostport
	}

	return host
}

// loopbackName reports whether host names the loopback interface.
func loopbackName(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}

	ip := net.ParseIP(strings.Trim(host, "[]"))

	return ip != nil && ip.IsLoopback()
}

// corsMiddleware admits same-origin requests and, when configured, exactly
// one external dashboard origin. A foreign Origin is rejected before it can
// trigger even a response-blind loopback side effect.
func corsMiddleware(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestOrigin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Add("Vary", "Origin")
		}
		if originlessCrossSiteAPI(r, requestOrigin) {
			http.Error(w, "cross-site API request not allowed", http.StatusForbidden)

			return
		}
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		if requestOrigin == "" || requestOrigin == scheme+"://"+r.Host {
			next.ServeHTTP(w, r)

			return
		}
		if origin == "" || requestOrigin != origin {
			http.Error(w, "origin not allowed", http.StatusForbidden)

			return
		}

		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)

		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Max-Age", "3600")
			w.WriteHeader(http.StatusNoContent)

			return
		}

		next.ServeHTTP(w, r)
	})
}

func originlessCrossSiteAPI(r *http.Request, requestOrigin string) bool {
	return requestOrigin == "" && r.Header.Get("Sec-Fetch-Site") == "cross-site" &&
		strings.HasPrefix(r.URL.Path, "/api/")
}

// rootHandler serves the wildcard data-plane paths and sends everything
// else to the dashboard; reserved slugs keep sessions from shadowing.
func rootHandler(data DataPlane) http.Handler {
	if data.Log == nil {
		data.Log = slog.New(slog.DiscardHandler)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Redirect(w, r, "/ui/", http.StatusTemporaryRedirect)

			return
		}

		// The legacy direct endpoints have exact leaves; everything else
		// under a slug routes to the HLS tree.
		if slug, lang, leaf, ok := mediaPath(r.URL.Path); ok {
			switch leaf {
			case "subs.vtt":
				serveSubs(w, r, data.Docs, slug, lang)

				return
			case "audio.ts":
				serveAudio(w, r, data.Streams, slug, lang)

				return
			}
		}

		if slug, rest, ok := hlsPath(r.URL.Path); ok {
			serveHLS(w, r, data.Media, data.Log, slug, rest)

			return
		}

		http.Redirect(w, r, "/ui/", http.StatusTemporaryRedirect)
	})
}

// mediaPath recognizes /{session}/{lang}/{leaf} data-plane paths.
func mediaPath(path string) (slug, lang, leaf string, ok bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}

	return parts[0], parts[1], parts[2], true
}

// hlsPath recognizes /{slug}/master.m3u8 and /{slug}/…/*.{m3u8,ts,vtt};
// Open validates the tree's shape, this only routes by suffix.
func hlsPath(path string) (slug, rest string, ok bool) {
	slug, rest, found := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	if !found || slug == "" || rest == "" {
		return "", "", false
	}

	if rest == hlsMaster {
		return slug, rest, true
	}

	if !strings.Contains(rest, "/") {
		return "", "", false
	}

	switch filepath.Ext(rest) {
	case ".m3u8", ".ts", ".vtt":
		return slug, rest, true
	default:
		return "", "", false
	}
}

// HLS content types by extension; playlists must not be cached (they roll).
const hlsMaster = "master.m3u8"

// serveHLS delivers the master playlist or one rendition file from the
// session's tree, refusing any path that escapes it.
func serveHLS(w http.ResponseWriter, r *http.Request, media MediaTree, logger *slog.Logger, slug, rest string) {
	if rest == hlsMaster {
		playlist, ok := media.MasterPlaylist(slug)
		if !ok {
			http.NotFound(w, r)

			return
		}

		header := w.Header()
		header.Set("Content-Type", "application/vnd.apple.mpegurl")
		header.Set("Cache-Control", "no-store")
		http.ServeContent(w, r, hlsMaster, time.Time{}, bytes.NewReader(playlist))

		return
	}

	header := w.Header()

	switch filepath.Ext(rest) {
	case ".m3u8":
		header.Set("Content-Type", "application/vnd.apple.mpegurl")
		header.Set("Cache-Control", "no-store")
	case ".ts":
		header.Set("Content-Type", "video/mp2t")
	case ".vtt":
		header.Set("Content-Type", "text/vtt; charset=utf-8")
		header.Set("Cache-Control", "no-store")
	default:
		http.NotFound(w, r)

		return
	}

	// The store opens the file itself from a path rebuilt out of its own
	// components, so request data never names anything on disk.
	f, ok := media.Open(slug, rest)
	if !ok {
		http.NotFound(w, r)

		return
	}

	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			// ServeContent already committed the response; the failure
			// can only be reported server-side.
			logger.Warn("closing media file", "session", slug, "err", closeErr)
		}
	}()

	http.ServeContent(w, r, rest, time.Time{}, f)
}

// serveAudio streams the live dubbed transport stream
// (/{session}/{lang}/audio.ts, the radio/interpreter feed).
func serveAudio(w http.ResponseWriter, r *http.Request, streams AudioStreams, slug, lang string) {
	header := w.Header()
	header.Set("Content-Type", "video/mp2t")
	header.Set("Cache-Control", "no-store")

	if !streams.ServeTS(r.Context(), w, slug, lang) {
		http.NotFound(w, r)
	}
}

// serveSubs delivers the live rolling WebVTT of one session and language.
func serveSubs(w http.ResponseWriter, r *http.Request, docs CaptionDocs, slug, lang string) {
	doc, ok := docs.Document(slug, lang)
	if !ok {
		http.NotFound(w, r)

		return
	}

	header := w.Header()
	header.Set("Content-Type", "text/vtt; charset=utf-8")
	header.Set("Cache-Control", "no-store")

	http.ServeContent(w, r, "subs.vtt", time.Time{}, bytes.NewReader(doc))
}

// requireControlToken guards mutations and sensitive reads that may contain
// local paths or provider configuration. Other read-only REST resources
// remain available on loopback.
func requireControlToken(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readOnly := r.Method == http.MethodGet || r.Method == http.MethodHead
		if !readOnly || sensitiveControlRead(r.URL.Path) {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				http.Error(w, "missing or invalid control token", http.StatusUnauthorized)

				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func sensitiveControlRead(path string) bool {
	switch path {
	case "/api/v1/config", "/api/v1/devices", "/api/v1/doctor":
		return true
	default:
		return false
	}
}

func controlAPIHandler(token string, next http.Handler) http.Handler {
	return requireControlToken(token, http.MaxBytesHandler(next, controlBodyMaxBytes))
}
