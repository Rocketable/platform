package harness

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// curl is the emulated shell's HTTP client: a GET through webfetch's client, so the agent's network rules
// apply to it and to every redirect (R34). It takes -s -S -f -L -o and their long forms, short ones combined
// (-fsSL); anything else fails rather than changing the request. -o writes only inside the agent's folder.
func curl(ctx context.Context, client *http.Client, folder string, args []string, cwd string, stdout, stderr io.Writer) int {
	c, msg := parseCurlArgs(args)
	if msg != "" {
		_, _ = fmt.Fprintf(stderr, "curl: %s (this curl fetches through webfetch and takes -s -S -f -L -o)\n", msg)

		return 2
	}

	failed := func(code int, err error) int {
		if !c.silent || c.showError {
			_, _ = fmt.Fprintf(stderr, "curl: (%d) %v\n", code, err)
		}

		return code
	}

	ctx, cancel := context.WithTimeout(ctx, defaultWebFetchTimeout)
	defer cancel()

	resp, err := doWebFetchRequest(ctx, client, c.target, "", false)
	if err != nil {
		return failed(7, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if c.fail && resp.StatusCode >= 400 {
		return failed(22, fmt.Errorf("the requested URL returned error: %d", resp.StatusCode))
	}

	w := stdout

	if c.output != "" {
		f, err := createInFolder(folder, cwd, c.output)
		if err != nil {
			return failed(23, err)
		}
		defer func() { _ = f.Close() }()

		w = f
	}

	// The body is written on the host, outside the sandbox's file limit, so it gets its own (spec §10.1).
	if _, err := io.Copy(w, io.LimitReader(resp.Body, maxCurlBytes)); err != nil {
		return failed(23, err)
	}

	if n, _ := resp.Body.Read(make([]byte, 1)); n > 0 {
		return failed(63, fmt.Errorf("response exceeds %d MiB", maxCurlBytes>>20))
	}

	return 0
}

// maxCurlBytes caps one curl response body.
const maxCurlBytes = 10 << 20

type curlArgs struct {
	target, output          string
	fail, silent, showError bool
}

// parseCurlArgs reads curl's arguments; a non-empty message is a usage error.
func parseCurlArgs(args []string) (c curlArgs, usage string) {
	for i := 0; i < len(args); i++ {
		a := args[i]

		if a == "--" && i+1 < len(args) {
			// Everything after -- is the URL, even when it starts with a dash.
			if c.target != "" || i+2 < len(args) {
				return c, "one URL only"
			}

			c.target = args[i+1]

			break
		}

		switch a {
		case "-s", "--silent":
			c.silent = true
		case "-S", "--show-error":
			c.showError = true
		case "-f", "--fail":
			c.fail = true
		case "-L", "--location": // webfetch's client follows redirects, checking each one
		case "-o", "--output":
			i++
			if i == len(args) {
				return c, "option " + a + " needs a file"
			}

			c.output = args[i]
		default:
			// A short cluster such as -fsSL; an o in it takes the rest of the cluster or the next argument.
			flags, file, hasFile := strings.Cut(strings.TrimPrefix(a, "-"), "o")

			switch {
			case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && len(a) > 1 && strings.Trim(flags, "sSfL") == "":
				c.silent = c.silent || strings.Contains(flags, "s")
				c.showError = c.showError || strings.Contains(flags, "S")
				c.fail = c.fail || strings.Contains(flags, "f")

				if hasFile && file == "" {
					i++
					if i == len(args) {
						return c, "option -o needs a file"
					}

					file = args[i]
				}

				if hasFile {
					c.output = file
				}
			case strings.HasPrefix(a, "-"):
				return c, "option " + a + " is not supported"
			case c.target != "":
				return c, "one URL only"
			default:
				c.target = a
			}
		}
	}

	if c.target == "" {
		return c, "no URL specified"
	}

	if !strings.Contains(c.target, "://") {
		c.target = "http://" + c.target
	}

	return c, ""
}

// createInFolder creates name, a guest path relative to cwd, through an os.Root on folder: no path or link
// takes it out of the agent's folder.
func createInFolder(folder, cwd, name string) (*os.File, error) {
	if !filepath.IsAbs(name) {
		name = filepath.Join(cwd, name)
	}

	rel, err := filepath.Rel(folder, name)
	if err != nil || !filepath.IsLocal(rel) {
		return nil, fmt.Errorf("%s: outside the agent's folder", name)
	}

	root, err := os.OpenRoot(folder)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", folder, err)
	}
	defer func() { _ = root.Close() }()

	f, err := root.Create(rel)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", name, err)
	}

	return f, nil
}
