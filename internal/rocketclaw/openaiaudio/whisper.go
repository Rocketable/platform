// Package openaiaudio contains OpenAI-backed audio helpers.
package openaiaudio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WhisperClient transcribes audio through the OpenAI transcription API.
type WhisperClient struct {
	apiKey, apiBase, model, prompt string
	httpClient                     *http.Client
}

type transcriptionResponse struct {
	Text string `json:"text"`
}

// NewWhisperClient constructs a transcription client with sane defaults.
func NewWhisperClient(apiKey, apiBase, model, prompt string) *WhisperClient {
	if strings.TrimSpace(model) == "" {
		model = "whisper-1"
	}

	return &WhisperClient{apiKey: apiKey, apiBase: NormalizeAudioURL(apiBase, "/audio/transcriptions"), model: model, prompt: prompt, httpClient: &http.Client{Timeout: 60 * time.Second}}
}

// Transcribe uploads the provided audio file and returns the recognized text.
func (c *WhisperClient) Transcribe(ctx context.Context, path string) (text string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open audio file: %w", err)
	}

	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close audio file: %w", closeErr)
		}
	}()

	var body bytes.Buffer

	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}

	if _, err := io.Copy(part, file); err != nil {
		return "", fmt.Errorf("copy audio file: %w", err)
	}

	for _, field := range [...][2]string{{"model", c.model}, {"response_format", "json"}, {"chunking_strategy", "auto"}} {
		if err := writer.WriteField(field[0], field[1]); err != nil {
			return "", fmt.Errorf("write %s: %w", field[0], err)
		}
	}

	if strings.TrimSpace(c.prompt) != "" {
		if err := writer.WriteField("prompt", c.prompt); err != nil {
			return "", fmt.Errorf("write prompt: %w", err)
		}
	}

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase, &body)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("send request: %w", err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("whisper API error (%d): %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var payload transcriptionResponse
	if err := json.Unmarshal(respBody, &payload); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	return strings.TrimSpace(payload.Text), nil
}

// NormalizeAudioURL appends an OpenAI audio endpoint suffix to an API base URL.
func NormalizeAudioURL(apiBase, suffix string) string {
	if strings.TrimSpace(apiBase) == "" {
		return "https://api.openai.com/v1" + suffix
	}

	u, err := url.Parse(apiBase)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.TrimRight(apiBase, "/") + suffix
	}

	path := strings.TrimRight(u.Path, "/")
	if u.Host == "api.openai.com" {
		if path == "" || path == "/v1" {
			path = "/v1" + suffix
		} else if !strings.HasPrefix(path, "/v1/") {
			path = "/v1" + path
		}

		if !strings.HasSuffix(path, suffix) {
			path += suffix
		}
	} else if !strings.HasSuffix(path, suffix) {
		path += suffix
	}

	u.Path = path

	return u.String()
}
