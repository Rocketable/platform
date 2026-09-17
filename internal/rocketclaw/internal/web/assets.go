// Package web serves the compiled browser application embedded in RocketClaw.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"slices"
	"strings"
)

//go:embed dist
var assets embed.FS

// Handler serves application routes and compiled assets without a frontend runtime.
func Handler() http.Handler {
	files, _ := fs.Sub(assets, "dist")
	server := http.FileServerFS(files)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")

		if slices.Contains([]string{"/", "/cron", "/agents", "/skills", "/config", "/settled"}, r.URL.Path) || strings.HasPrefix(r.URL.Path, "/s/") {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}

		server.ServeHTTP(w, r)
	})
}
