// Package web serves the compiled browser application embedded in RocketClaw.
package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
)

//go:embed dist
var assets embed.FS

// sentryBootstrap is the browser SDK configuration embedded in the index page.
type sentryBootstrap struct {
	config.SentryConfig

	Release string `json:"release,omitempty"`
}

// release names the running module version so browser reports can be compared across deploys.
// Local builds without a module version report none.
func release(build *debug.BuildInfo) string {
	if build == nil || build.Main.Version == "(devel)" {
		return ""
	}

	return build.Main.Version
}

// Handler serves application routes and compiled assets without a frontend runtime.
func Handler(sentry config.SentryConfig) http.Handler {
	files, _ := fs.Sub(assets, "dist")
	server := http.FileServerFS(files)

	var index []byte
	if sentry.DSN != "" {
		// The embedded index exists, and validated JSON config contains only serializable values.
		index, _ = fs.ReadFile(files, "index.html")
		build, _ := debug.ReadBuildInfo()
		data, _ := json.Marshal(sentryBootstrap{SentryConfig: sentry, Release: release(build)}) // HTML escaping prevents closing the script element.
		index = []byte(strings.Replace(string(index), "</head>", `<script id="sentry-config" type="application/json">`+string(data)+`</script></head>`, 1))
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")

		if slices.Contains([]string{"/", "/cron", "/agents", "/skills", "/config", "/search", "/voice"}, r.URL.Path) || strings.HasPrefix(r.URL.Path, "/s/") || strings.HasPrefix(r.URL.Path, "/skills/") {
			if sentry.DSN != "" {
				http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))

				return
			}

			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}

		server.ServeHTTP(w, r)
	})
}
