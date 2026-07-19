// Package webui embeds the dashboard: a Svelte SPA built from web/ into
// dist/ by `make web`; Node is build-time only.
package webui

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"time"
)

//go:embed dist
var dist embed.FS

// Handler serves the embedded dashboard.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// The dist tree is embedded at compile time from a literal path;
		// failing here is a programmer error, not a runtime condition.
		panic(err)
	}

	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic(err)
	}
	const hostedMarker = `name="prukka-api-base" content="http://127.0.0.1:8080"`
	const embeddedMarker = `name="prukka-api-base" content="same-origin"`
	embeddedIndex := bytes.Replace(index, []byte(hostedMarker), []byte(embeddedMarker), 1)
	if bytes.Equal(embeddedIndex, index) {
		panic("embedded dashboard lacks the API mode marker")
	}

	files := http.FileServerFS(sub)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Asset names are stable across releases (app.js/index.css), so a
		// heuristic browser cache could otherwise pair an old privileged UI
		// with a newer control API.
		w.Header().Set("Cache-Control", "no-store")
		if (r.URL.Path == "/" || r.URL.Path == "/index.html") &&
			(r.Method == http.MethodGet || r.Method == http.MethodHead) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(embeddedIndex))

			return
		}

		files.ServeHTTP(w, r)
	})
}
