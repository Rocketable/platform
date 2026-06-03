package main

import (
	"context"
	"fmt"
	"io"
	"net"
)

func prepareToServe(ctx context.Context, store store, id string, out io.Writer) error {
	iv, err := store.loadInterview(id)
	if err != nil {
		return err
	}
	if len(iv.Questions) == 0 {
		return fmt.Errorf("interview %q has no questions", id)
	}
	fmt.Fprint(out, "Rendering HTML from Markdown... ")
	client := newMarkdownClient()
	rendered := make([]string, len(iv.Questions))
	for i, q := range iv.Questions {
		html, err := client.render(ctx, q.Body)
		if err != nil {
			return err
		}
		rendered[i] = html
	}
	fmt.Fprintln(out, "DONE")
	port, err := chooseFreePort()
	if err != nil {
		return err
	}
	prepared := &Prepared{Port: port, RenderedHTML: rendered}
	if err := store.savePrepared(id, prepared); err != nil {
		return err
	}
	iv.Prepared = true
	if err := store.saveInterview(iv); err != nil {
		return err
	}
	fmt.Fprintln(out, "Serving at (in order of preference):")
	for _, addr := range servingAddresses(port, id) {
		fmt.Fprintf(out, "- %s\n", addr)
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "CRITICAL: you must call `./interviewd %q wait-for-user` to collect answers. The agent tool call should use a very long timeout, suggested: 1h.\n", id)
	return nil
}

func chooseFreePort() (int, error) {
	ln, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}
