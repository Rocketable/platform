// Package web serves the compiled browser application embedded in RocketClaw.
package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"html"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
)

//go:embed dist all:oc-dist
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
// A non-empty serverName prefixes the page title so browser tabs tell servers apart.
func Handler(sentry config.SentryConfig, posthog config.PostHogConfig, serverName string) http.Handler {
	files, _ := fs.Sub(assets, "dist")
	ocFiles, _ := fs.Sub(assets, "oc-dist")

	// The embedded index exists.
	page, _ := fs.ReadFile(files, "index.html")
	index := string(page)

	if serverName != "" {
		index = strings.Replace(index, "<title>RocketClaw</title>", "<title>"+html.EscapeString(serverName)+" - RocketClaw</title>", 1)
	}

	if sentry.DSN != "" {
		// Validated JSON config contains only serializable values.
		build, _ := debug.ReadBuildInfo()
		data, _ := json.Marshal(sentryBootstrap{SentryConfig: sentry, Release: release(build)}) // HTML escaping prevents closing the script element.
		index = strings.Replace(index, "</head>", `<script id="sentry-config" type="application/json">`+string(data)+`</script></head>`, 1)
	}

	if posthog.ProjectToken != "" {
		data, _ := json.Marshal(posthog)
		index = strings.Replace(index, "</head>", `<script id="posthog-config" type="application/json">`+string(data)+`</script></head>`, 1)
	}

	page = []byte(index)

	var gzipWriters sync.Pool

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")

		if slices.Contains([]string{"/", "/cron", "/agents", "/skills", "/config", "/search", "/voice", "/pages", "/shortcuts"}, r.URL.Path) || strings.HasPrefix(r.URL.Path, "/search/") || strings.HasPrefix(r.URL.Path, "/s/") || strings.HasPrefix(r.URL.Path, "/oc/") || strings.HasPrefix(r.URL.Path, "/skills/") || strings.HasPrefix(r.URL.Path, "/pages/") || strings.HasPrefix(r.URL.Path, "/cron/") {
			serveFile(w, r, "index.html", page, "assets", &gzipWriters)

			return
		}

		root, name, immutable := files, strings.TrimPrefix(r.URL.Path, "/"), "assets"
		if ocName, ok := strings.CutPrefix(r.URL.Path, "/oc-ui/"); ok {
			root, name, immutable = ocFiles, ocName, "_assets"
		}

		data, err := fs.ReadFile(root, name)
		if err != nil {
			http.NotFound(w, r)

			return
		}

		serveFile(w, r, name, data, immutable, &gzipWriters)
	})
}

// hashed matches a built file's name that carries its content hash.
var hashed = regexp.MustCompile(`-[A-Za-z0-9_-]{8,}\.[a-z0-9]+$`)

// serveFile sends a built file's data with an ETag, gzip-compressed for browsers that accept it. A file in the immutable
// directory named for its content never changes under that name, so browsers keep it without asking again.
func serveFile(w http.ResponseWriter, r *http.Request, name string, data []byte, immutable string, writers *sync.Pool) {
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

			writer, ok := writers.Get().(*gzip.Writer)
			if !ok {
				writer = gzip.NewWriter(io.Discard)
			}

			writer.Reset(&compressed)
			_, _ = writer.Write(data)
			_ = writer.Close()
			// Keep the compressor, not this response's buffer.
			writer.Reset(io.Discard)
			writers.Put(writer)

			data, tag = compressed.Bytes(), tag+"-gzip"

			w.Header().Set("Content-Encoding", "gzip")
		}
	}

	w.Header().Set("ETag", `"`+tag+`"`)
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}
