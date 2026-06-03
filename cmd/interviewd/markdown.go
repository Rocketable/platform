package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

type markdownClient struct {
	endpoint string
	client   *http.Client
}

func newMarkdownClient() markdownClient {
	endpoint := os.Getenv("INTERVIEWD_GITHUB_MARKDOWN_ENDPOINT")
	if endpoint == "" {
		endpoint = "https://api.github.com/markdown"
	}
	return markdownClient{endpoint: endpoint, client: &http.Client{Timeout: 30 * time.Second}}
}

func (c markdownClient) render(ctx context.Context, text string) (string, error) {
	body, err := json.Marshal(map[string]string{"text": text, "mode": "gfm"})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub Markdown API returned %s: %s", resp.Status, string(data))
	}
	return string(data), nil
}
