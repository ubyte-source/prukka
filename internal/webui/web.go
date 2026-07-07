// Package webui embeds the dashboard: a Svelte SPA built from web/ into
// dist/ by `make web`; Node is build-time only.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
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

	return http.FileServerFS(sub)
}
