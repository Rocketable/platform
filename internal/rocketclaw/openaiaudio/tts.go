package openaiaudio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// TTSClient synthesizes speech through the OpenAI audio speech API.
type TTSClient struct {
	apiKey, apiBase, model, voice, instructions string
	httpClient                                  *http.Client
}

type speechRequest struct {
	Model          string `json:"model"`
	Input          string `json:"input"`
	Voice          string `json:"voice"`
	Instructions   string `json:"instructions,omitempty"`
	ResponseFormat string `json:"response_format"`
}

// NewTTSClient constructs a TTS client with sane defaults for missing model values.
func NewTTSClient(apiKey, apiBase, model, voice, instructions string) *TTSClient {
	if strings.TrimSpace(model) == "" {
		model = "tts-1"
	}

	if strings.TrimSpace(voice) == "" {
		voice = "alloy"
	}

	return &TTSClient{apiKey: apiKey, apiBase: NormalizeAudioURL(apiBase, "/audio/speech"), model: model, voice: voice, instructions: instructions, httpClient: &http.Client{Timeout: 60 * time.Second}}
}

// Synthesize sends text to the speech API and returns the streamed audio body.
func (c *TTSClient) Synthesize(ctx context.Context, text string) (io.ReadCloser, error) {
	return c.SynthesizeFormat(ctx, text, "opus")
}

// SynthesizeFormat sends text to the speech API using the requested format and returns the streamed audio body.
func (c *TTSClient) SynthesizeFormat(ctx context.Context, text, responseFormat string) (io.ReadCloser, error) {
	instructions := ""
	if strings.TrimSpace(c.instructions) != "" {
		instructions = c.instructions
	}

	responseFormat = strings.TrimSpace(strings.ToLower(responseFormat))
	if responseFormat == "" {
		responseFormat = "opus"
	}

	payload := speechRequest{Model: c.model, Input: text, Voice: c.voice, Instructions: instructions, ResponseFormat: responseFormat}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal TTS request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create TTS request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send TTS request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer func() {
			_ = resp.Body.Close()
		}()

		body, _ := io.ReadAll(resp.Body)

		return nil, fmt.Errorf("tts API error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return resp.Body, nil
}
