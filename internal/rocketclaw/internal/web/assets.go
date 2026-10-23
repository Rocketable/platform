// Package web serves the compiled browser application embedded in RocketClaw.
package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
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

		name := strings.TrimPrefix(r.URL.Path, "/")

		if slices.Contains([]string{"/", "/cron", "/agents", "/skills", "/config", "/search", "/voice", "/pages", "/shortcuts"}, r.URL.Path) || strings.HasPrefix(r.URL.Path, "/s/") || strings.HasPrefix(r.URL.Path, "/oc/") || strings.HasPrefix(r.URL.Path, "/skills/") || strings.HasPrefix(r.URL.Path, "/pages/") {
			if sentry.DSN != "" {
				http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))

				return
			}

			name = "index.html"
		}

		serveFile(w, r, files, name, "assets")
	})
}

// hashed matches a built file's name that carries its content hash.
var hashed = regexp.MustCompile(`-[A-Za-z0-9_-]{8,}\.[a-z0-9]+$`)

// serveFile sends a built file with an ETag, gzip-compressed for browsers that accept it. A file in the immutable
// directory named for its content never changes under that name, so browsers keep it without asking again.
func serveFile(w http.ResponseWriter, r *http.Request, files fs.FS, name, immutable string) {
	data, err := fs.ReadFile(files, name)
	if err != nil {
		http.NotFound(w, r)

		return
	}

	if path.Dir(name) == immutable && hashed.MatchString(path.Base(name)) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}

	// The type comes from the uncompressed bytes; ServeContent would sniff the compressed ones.
	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}

	w.Header().Set("Content-Type", contentType)

	sum := sha256.Sum256(data)
	tag := hex.EncodeToString(sum[:8])

	if slices.ContainsFunc([]string{"text/", "javascript", "json", "svg", "wasm", "font/ttf", "font/otf"}, func(kind string) bool { return strings.Contains(contentType, kind) }) {
		w.Header().Add("Vary", "Accept-Encoding")

		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			var compressed bytes.Buffer

			writer := gzip.NewWriter(&compressed)
			_, _ = writer.Write(data)
			_ = writer.Close()
			data, tag = compressed.Bytes(), tag+"-gzip"

			w.Header().Set("Content-Encoding", "gzip")
		}
	}

	w.Header().Set("ETag", `"`+tag+`"`)
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}
