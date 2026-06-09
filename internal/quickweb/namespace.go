package quickweb

import (
	"errors"
	"net/url"
	"path"
	"strings"
)

func (s *Server) namespaceForPath(raw string) (string, error) {
	return normalizeNamespace(raw, s.root)
}

func normalizeNamespace(raw string, root *osRoot) (string, error) {
	raw = strings.TrimSpace(raw)
	if idx := strings.IndexAny(raw, "?#"); idx >= 0 {
		raw = raw[:idx]
	}

	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", err
	}

	if strings.Contains(decoded, "\x00") || strings.Contains(decoded, "\\") {
		return "", errors.New("invalid path")
	}

	hadTrailingSlash := strings.HasSuffix(decoded, "/")
	trimmed := strings.TrimLeft(decoded, "/")
	if escapesRoot(trimmed) {
		return "", errors.New("path escapes root")
	}

	cleaned := path.Clean("/" + trimmed)
	name := strings.TrimPrefix(cleaned, "/")
	if name == "" || name == "." {
		return "index.html", nil
	}

	if hadTrailingSlash {
		return path.Join(name, "index.html"), nil
	}

	if root != nil {
		if info, err := root.Stat(name); err == nil && info.IsDir() {
			if indexInfo, err := root.Stat(path.Join(name, "index.html")); err == nil && indexInfo.Mode().IsRegular() {
				return path.Join(name, "index.html"), nil
			}
		}
	}

	return name, nil
}

func escapesRoot(name string) bool {
	depth := 0
	for _, part := range strings.Split(name, "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			if depth == 0 {
				return true
			}
			depth--
		default:
			depth++
		}
	}

	return false
}
