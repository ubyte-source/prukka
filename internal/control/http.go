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
	"path"
	"strings"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/protobuf/encoding/protojson"

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
	engine     *Engine
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

	// The gateway's stock marshaler discards unknown JSON fields, which turns
	// a misspelled key into a silent no-op with the rest of the document
	// applied; every other document boundary in the daemon names it instead.
	// The wrapper and EmitUnpopulated are the library defaults: only the
	// inbound leniency goes away, so read shapes are unchanged.
	gw := runtime.NewServeMux(runtime.WithMarshalerOption(runtime.MIMEWildcard,
		&runtime.HTTPBodyMarshaler{Marshaler: &runtime.JSONPb{
			MarshalOptions:   protojson.MarshalOptions{EmitUnpopulated: true},
			UnmarshalOptions: protojson.UnmarshalOptions{DiscardUnknown: false},
		}},
	))
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

	mux.Handle("/api/v1/events", sseHandler(d.store, d.log, d.dubbed, d.engine, d.token))
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
//
// The leaves are ServeMux patterns rather than a hand-rolled path parser,
// and that is what makes HEAD correct: a pattern with the method GET matches
// both GET and HEAD, so a player, CDN origin or uptime monitor probing a
// segment gets the resource's headers. The mux is nested under the server's
// own "/" pattern instead of merged into it because a method-scoped wildcard
// like "GET /{session}/{lang}/subs.vtt" fails registration beside the
// method-less literal subtrees it would sit next to (/ui/, /api/v1/):
// ServeMux precedence only resolves strict-subset overlaps.
func rootHandler(data DataPlane) http.Handler {
	if data.Log == nil {
		data.Log = slog.New(slog.DiscardHandler)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{session}/{lang}/subs.vtt", func(w http.ResponseWriter, r *http.Request) {
		serveSubs(w, r, data.Docs, r.PathValue("session"), r.PathValue("lang"))
	})
	mux.HandleFunc("GET /{session}/{lang}/audio.ts", func(w http.ResponseWriter, r *http.Request) {
		serveAudio(w, r, data.Streams, r.PathValue("session"), r.PathValue("lang"))
	})
	mux.HandleFunc("GET /{slug}/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		serveSessionTree(w, r, &data)
	})
	// Unknown paths and every method the data plane does not answer.
	mux.HandleFunc("/", dashboardRedirect)

	return mux
}

// dashboardRedirect sends a request the data plane does not serve to the
// operator dashboard.
func dashboardRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/ui/", http.StatusTemporaryRedirect)
}

// serveSessionTree answers one file of a session's HLS tree. A suffix
// outside hlsFileTypes names no rendition at all, so it keeps the
// destination every unrecognized path has: the dashboard.
func serveSessionTree(w http.ResponseWriter, r *http.Request, data *DataPlane) {
	rest := r.PathValue("rest")
	kind, servable := hlsFileTypes[path.Ext(rest)]
	if !servable {
		dashboardRedirect(w, r)

		return
	}

	serveHLS(w, r, data, kind, r.PathValue("slug"), rest)
}

const hlsMaster = "master.m3u8"

// hlsFileType is how one rendition file is answered; playlists must not be
// cached because they roll.
type hlsFileType struct {
	contentType string
	noStore     bool
}

// hlsFileTypes is the single whitelist of servable HLS extensions:
// serveSessionTree routes by it and serveHLS answers with it.
var hlsFileTypes = map[string]hlsFileType{
	".m3u8": {contentType: "application/vnd.apple.mpegurl", noStore: true},
	".ts":   {contentType: "video/mp2t"},
	".vtt":  {contentType: "text/vtt; charset=utf-8", noStore: true},
}

// serveHLS delivers the master playlist or one rendition file from the
// session's tree, refusing any path that escapes it.
func serveHLS(
	w http.ResponseWriter, r *http.Request, data *DataPlane, kind hlsFileType, slug, rest string,
) {
	header := w.Header()
	header.Set("Content-Type", kind.contentType)
	if kind.noStore {
		header.Set("Cache-Control", "no-store")
	}

	if rest == hlsMaster {
		playlist, ok := data.Media.MasterPlaylist(slug)
		if !ok {
			http.NotFound(w, r)

			return
		}

		http.ServeContent(w, r, hlsMaster, time.Time{}, bytes.NewReader(playlist))

		return
	}

	// The store opens the file itself from a path rebuilt out of its own
	// components, so request data never names anything on disk.
	f, ok := data.Media.Open(slug, rest)
	if !ok {
		http.NotFound(w, r)

		return
	}

	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			// ServeContent already committed the response; the failure
			// can only be reported server-side.
			data.Log.Warn("closing media file", "session", slug, "err", closeErr)
		}
	}()

	http.ServeContent(w, r, rest, time.Time{}, f)
}

// serveAudio streams the live dubbed transport stream
// (/{session}/{lang}/audio.ts, the radio/interpreter feed).
//
// HEAD answers from the headers alone. ServeTS blocks for the whole life of
// a listener and takes a playout slot to do it, while net/http discards
// every body write on a HEAD: entering it for a probe would start a mux that
// streams into nothing. The cost is that HEAD cannot report an unknown pair,
// because knowing would mean opening the stream it is avoiding.
func serveAudio(w http.ResponseWriter, r *http.Request, streams AudioStreams, slug, lang string) {
	header := w.Header()
	header.Set("Content-Type", "video/mp2t")
	header.Set("Cache-Control", "no-store")

	if r.Method == http.MethodHead {
		return
	}

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

func sensitiveControlRead(route string) bool {
	switch route {
	case "/api/v1/config", "/api/v1/devices", "/api/v1/doctor", "/api/v1/engine":
		return true
	default:
		return false
	}
}

func controlAPIHandler(token string, next http.Handler) http.Handler {
	return requireControlToken(token, http.MaxBytesHandler(next, controlBodyMaxBytes))
}
