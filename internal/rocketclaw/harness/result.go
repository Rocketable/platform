package harness

import (
	"path/filepath"

	"go.starlark.net/starlark"
)

type codeModeCallDetails string

func (tool codeModeCallDetails) Details(args map[string]any) starlark.StringDict {
	text := func(name string) starlark.Value { return starlark.String(argString(args, name)) }

	switch tool {
	case "read":
		path := argString(args, "filePath")
		if path == "" {
			return starlark.StringDict{"path": starlark.String(""), "name": starlark.String("")}
		}

		return starlark.StringDict{"path": starlark.String(path), "name": starlark.String(filepath.Base(path))}
	case "glob":
		return starlark.StringDict{"pattern": text("pattern"), "path": text("path")}
	case "grep":
		return starlark.StringDict{"pattern": text("pattern"), "path": text("path"), "include": text("include")}
	case "webfetch":
		return starlark.StringDict{"url": text("url")}
	case "decision":
		return starlark.StringDict{"model": text("model")}
	case "bash":
		return starlark.StringDict{"output": starlark.String(""), "error_code": starlark.String("")}
	default:
		return nil
	}
}
