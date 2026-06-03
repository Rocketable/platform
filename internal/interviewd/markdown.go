package interviewd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type markdownClient struct {
	endpoint string
	client   *http.Client
}

type markdownRequest struct {
	Text string `json:"text"`
	Mode string `json:"mode"`
}

func (c markdownClient) render(ctx context.Context, text string) (string, error) {
	body, err := json.Marshal(markdownRequest{Text: text, Mode: "gfm"})
	if err != nil {
		return "", fmt.Errorf("marshal markdown request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create markdown request: %w", err)
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Github-Api-Version", "2026-03-10")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("render markdown: %w", err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read markdown response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub Markdown API returned %s: %s", resp.Status, string(data))
	}

	return string(data), nil
}
