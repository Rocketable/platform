package quickweb

import (
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"
)

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, http.MethodGet, http.MethodHead)
		return
	}

	name, hadTrailingSlash, err := cleanStaticPath(r.URL.EscapedPath())
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if name == "" {
		name = "index.html"
	} else if hadTrailingSlash {
		name = path.Join(name, "index.html")
	} else {
		if info, err := s.root.Stat(name); err == nil && info.IsDir() {
			if indexInfo, err := s.root.Stat(path.Join(name, "index.html")); err == nil && indexInfo.Mode().IsRegular() {
				redirectPath := r.URL.Path + "/"
				if r.URL.RawQuery != "" {
					redirectPath += "?" + r.URL.RawQuery
				}
				http.Redirect(w, r, redirectPath, http.StatusPermanentRedirect)
				return
			}
		}
	}

	if isBlockedStaticPath(name) {
		http.NotFound(w, r)
		return
	}

	file, err := s.root.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}

	http.ServeContent(w, r, path.Base(name), info.ModTime(), file)
}

func cleanStaticPath(raw string) (name string, hadTrailingSlash bool, err error) {
	if raw == "" {
		raw = "/"
	}

	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", false, err
	}

	if strings.Contains(decoded, "\x00") || strings.Contains(decoded, "\\") {
		return "", false, errors.New("invalid static path")
	}

	hadTrailingSlash = strings.HasSuffix(decoded, "/")
	parts := strings.Split(strings.TrimPrefix(decoded, "/"), "/")
	for _, part := range parts {
		if part == ".." {
			return "", false, errors.New("static path traversal")
		}
	}

	cleaned := path.Clean("/" + strings.TrimLeft(decoded, "/"))
	if cleaned == "/" {
		return "", hadTrailingSlash, nil
	}

	return strings.TrimPrefix(cleaned, "/"), hadTrailingSlash, nil
}

func isBlockedStaticPath(name string) bool {
	if name == "" {
		return false
	}

	for _, segment := range strings.Split(name, "/") {
		if segment == "" {
			continue
		}

		if strings.HasPrefix(segment, ".") {
			return true
		}

		if strings.HasSuffix(segment, ".sqlite") || strings.Contains(segment, ".sqlite-") {
			return true
		}
	}

	return false
}
