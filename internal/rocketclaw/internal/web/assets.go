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
	"maps"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
)

//go:generate sh -c "cd ../../web && bun install --frozen-lockfile && bun run build"
//go:generate sh -c "cd ../../oc && bun install --frozen-lockfile && OPENCODE_CHANNEL=prod bun --bun vite build"
//go:generate go run gzip.go dist oc-dist
//go:embed dist all:oc-dist
var assets embed.FS

func Handler(cfg *config.LockedConfig, serverName string) http.Handler {
	contentTypes := map[string]string{
		".aac":   "audio/aac",
		".css":   "text/css; charset=utf-8",
		".html":  "text/html; charset=utf-8",
		".js":    "text/javascript; charset=utf-8",
		".svg":   "image/svg+xml",
		".ttf":   "font/ttf",
		".txt":   "text/plain; charset=utf-8",
		".wasm":  "application/wasm",
		".woff":  "font/woff",
		".woff2": "font/woff2",
	}

	tags := map[string]string{}
	_ = fs.WalkDir(assets, ".", func(name string, entry fs.DirEntry, _ error) error {
		if !entry.IsDir() {
			data, _ := assets.ReadFile(name)
			sum := sha256.Sum256(data)
			tags[name] = hex.EncodeToString(sum[:8])
		}

		return nil
	})

	page := string(unpack("dist/index.html.gz"))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")

		if slices.Contains([]string{"/", "/routines", "/agents", "/skills", "/config", "/search", "/voice", "/pages", "/shortcuts", "/closed"}, r.URL.Path) || strings.HasPrefix(r.URL.Path, "/search/") || strings.HasPrefix(r.URL.Path, "/s/") || strings.HasPrefix(r.URL.Path, "/r/") || strings.HasPrefix(r.URL.Path, "/oc/") || strings.HasPrefix(r.URL.Path, "/skills/") || strings.HasPrefix(r.URL.Path, "/agents/") || strings.HasPrefix(r.URL.Path, "/pages/") || strings.HasPrefix(r.URL.Path, "/routines/") {
			var (
				posthog     config.PostHogConfig
				rocketclaws map[string]config.RemoteRocketClawConfig
			)

			_ = cfg.Do(func(live *config.Config) error {
				posthog, rocketclaws = live.PostHog, maps.Clone(live.RocketClaws)
				return nil
			})

			index := page

			if serverName != "" {
				index = strings.Replace(index, "<title>RocketClaw</title>", "<title>"+html.EscapeString(serverName)+" - RocketClaw</title>", 1)
			}

			if posthog.ProjectToken != "" {
				data, _ := json.Marshal(posthog)
				index = strings.Replace(index, "</head>", `<script id="posthog-config" type="application/json">`+string(data)+`</script></head>`, 1)
			}

			if len(rocketclaws) > 0 {
				type remote struct {
					Name string `json:"name"`
					URL  string `json:"url"`
				}

				list := make([]remote, 0, len(rocketclaws))
				for _, name := range slices.Sorted(maps.Keys(rocketclaws)) {
					list = append(list, remote{name, rocketclaws[name].URL})
				}

				data, _ := json.Marshal(list)
				index = strings.Replace(index, "</head>", `<script id="rocketclaws" type="application/json">`+string(data)+`</script></head>`, 1)
			}

			sum := sha256.Sum256([]byte(index))
			serveFile(w, r, contentTypes[".html"], hex.EncodeToString(sum[:8]), strings.NewReader(index))

			return
		}

		root, name, immutable := "dist/", strings.TrimPrefix(r.URL.Path, "/"), "assets"
		if ocName, ok := strings.CutPrefix(r.URL.Path, "/oc-ui/"); ok {
			root, name, immutable = "oc-dist/", ocName, "_assets"
		}

		stored := root + name + ".gz"

		tag, ok := tags[stored]
		if !ok {
			http.NotFound(w, r)

			return
		}

		if path.Dir(name) == immutable && hashed.MatchString(path.Base(name)) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}

		w.Header().Add("Vary", "Accept-Encoding")

		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			serveFile(w, r, contentTypes[path.Ext(name)], tag, bytes.NewReader(unpack(stored)))

			return
		}

		file, _ := assets.Open(stored)

		w.Header().Set("Content-Encoding", "gzip")
		serveFile(w, r, contentTypes[path.Ext(name)], tag+"-gzip", file.(io.ReadSeeker))
	})
}

var hashed = regexp.MustCompile(`-[A-Za-z0-9_-]{8,}\.[a-z0-9]+$`)

func unpack(name string) []byte {
	file, _ := assets.Open(name)
	reader, _ := gzip.NewReader(file)
	data, _ := io.ReadAll(reader)

	return data
}

func serveFile(w http.ResponseWriter, r *http.Request, contentType, tag string, content io.ReadSeeker) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("ETag", `"`+tag+`"`)
	http.ServeContent(w, r, "", time.Time{}, content)
}
