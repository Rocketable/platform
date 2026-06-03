package interviewd

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

func prepareToServe(ctx context.Context, store store, id, cmd string, out io.Writer) error {
	iv, err := store.loadInterview(id)
	if err != nil {
		return err
	}

	if len(iv.Questions) == 0 {
		return fmt.Errorf("interview %q has no questions", id)
	}

	if _, err := fmt.Fprint(out, "Rendering HTML from Markdown... "); err != nil {
		return fmt.Errorf("write render status: %w", err)
	}

	client := markdownClient{endpoint: "https://api.github.com/markdown", client: &http.Client{Timeout: 30 * time.Second}}

	rendered := make([]string, len(iv.Questions))
	for i, q := range iv.Questions {
		html, err := client.render(ctx, q.Body)
		if err != nil {
			return err
		}

		rendered[i] = html
	}

	if _, err := fmt.Fprintln(out, "DONE"); err != nil {
		return fmt.Errorf("write render status: %w", err)
	}

	port, err := chooseFreePort()
	if err != nil {
		return err
	}

	prepared := &prepared{Port: port, RenderedHTML: rendered}
	if err := store.savePrepared(id, prepared); err != nil {
		return err
	}

	iv.Prepared = true
	if err := store.saveInterview(iv); err != nil {
		return err
	}

	if _, err := fmt.Fprintln(out, "Serving at (in order of preference):"); err != nil {
		return fmt.Errorf("write serving addresses: %w", err)
	}

	for _, addr := range servingAddresses(port, id) {
		if _, err := fmt.Fprintf(out, "- %s\n", addr); err != nil {
			return fmt.Errorf("write serving address: %w", err)
		}
	}

	if _, err := fmt.Fprintln(out); err != nil {
		return fmt.Errorf("write wait instruction: %w", err)
	}

	if _, err := fmt.Fprintf(out, "CRITICAL: you must call `%s %q wait-for-user` to collect answers. The agent tool call should use a very long timeout, suggested: 1h.\n", cmd, id); err != nil {
		return fmt.Errorf("write wait instruction: %w", err)
	}

	return nil
}

func chooseFreePort() (int, error) {
	ln, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		return 0, fmt.Errorf("listen on a free port: %w", err)
	}

	defer func() {
		_ = ln.Close()
	}()

	return ln.Addr().(*net.TCPAddr).Port, nil
}
