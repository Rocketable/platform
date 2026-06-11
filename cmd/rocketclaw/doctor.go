package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/openaiaudio"
)

func runDoctor(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "tts":
			return runDoctorTTS(args[1:])
		case "stt":
			return runDoctorSTT(args[1:])
		}
	}

	flagSet := flag.NewFlagSet("doctor", flag.ContinueOnError)
	if err := flagSet.Parse(args); err != nil {
		return fmt.Errorf("parse doctor flags: %w", err)
	}

	selected, cfg, err := loadRuntimeConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	lines := []string{
		fmt.Sprintf("Configuration: OK (%s)", selected.Path),
		"Workspace: " + cfg.Workspace,
		"Work directory: " + cfg.WorkDirName(),
		fmt.Sprintf("Discord Text: %t", cfg.DiscordText.Enabled),
		fmt.Sprintf("Discord Voice: %t", cfg.DiscordVoice.Enabled),
		fmt.Sprintf("Slack: %t", cfg.Slack.Enabled),
		"RocketCode: OK (library)",
	}

	if _, err := fmt.Fprint(os.Stdout, strings.Join(lines, "\n")+"\n"); err != nil {
		return fmt.Errorf("write doctor output: %w", err)
	}

	return nil
}

func runDoctorTTS(args []string) error {
	flagSet := flag.NewFlagSet("doctor tts", flag.ContinueOnError)

	text := flagSet.String("text", "Hello, Hally.", "text to synthesize")
	if err := flagSet.Parse(args); err != nil {
		return fmt.Errorf("parse doctor tts flags: %w", err)
	}

	if flagSet.NArg() != 0 {
		return errors.New("doctor tts does not accept positional arguments")
	}

	_, cfg, err := loadRuntimeConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx := context.Background()

	directElapsed, directBytes, directErr := doctorTTSDirect(ctx, cfg, *text)
	if err := writeDoctorProbeLine("direct", directElapsed, "bytes", directBytes, directErr); err != nil {
		return err
	}

	clientElapsed, clientBytes, clientErr := doctorTTSClient(ctx, cfg, *text)
	if err := writeDoctorProbeLine("client", clientElapsed, "bytes", clientBytes, clientErr); err != nil {
		return err
	}

	if directErr != nil || clientErr != nil {
		return exitCodeError(1)
	}

	return nil
}

func runDoctorSTT(args []string) error {
	flagSet := flag.NewFlagSet("doctor stt", flag.ContinueOnError)

	filePath := flagSet.String("file", "", "audio file to transcribe")
	if err := flagSet.Parse(args); err != nil {
		return fmt.Errorf("parse doctor stt flags: %w", err)
	}

	if flagSet.NArg() != 0 {
		return errors.New("doctor stt does not accept positional arguments")
	}

	if strings.TrimSpace(*filePath) == "" {
		return errors.New("doctor stt requires -file")
	}

	_, cfg, err := loadRuntimeConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx := context.Background()

	directElapsed, directLen, directErr := doctorSTTDirect(ctx, cfg, *filePath)
	if err := writeDoctorProbeLine("direct", directElapsed, "text_len", directLen, directErr); err != nil {
		return err
	}

	clientElapsed, clientLen, clientErr := doctorSTTClient(ctx, cfg, *filePath)
	if err := writeDoctorProbeLine("client", clientElapsed, "text_len", clientLen, clientErr); err != nil {
		return err
	}

	if directErr != nil || clientErr != nil {
		return exitCodeError(1)
	}

	return nil
}

func doctorTTSDirect(ctx context.Context, cfg *config.Config, text string) (time.Duration, int, error) {
	start := time.Now()

	model := cfg.OpenAI.TTSModel
	if strings.TrimSpace(model) == "" {
		model = "tts-1"
	}

	voice := cfg.OpenAI.TTSVoice
	if strings.TrimSpace(voice) == "" {
		voice = "alloy"
	}

	instructions := ""
	if strings.TrimSpace(cfg.OpenAI.TTSInstructions) != "" {
		instructions = cfg.OpenAI.TTSInstructions
	}

	payload := map[string]string{"model": model, "input": text, "voice": voice, "response_format": "opus"}
	if instructions != "" {
		payload["instructions"] = instructions
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return time.Since(start), 0, fmt.Errorf("marshal TTS request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openaiaudio.NormalizeAudioURL(cfg.OpenAI.TTSAPIBaseURL, "/audio/speech"), bytes.NewReader(body))
	if err != nil {
		return time.Since(start), 0, fmt.Errorf("create TTS request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.OpenAI.TTSAPIKey)

	httpClient, err := doctorOpenAIHTTPClient(cfg.OpenAI.TTSAPIKey, 2*time.Minute)
	if err != nil {
		return time.Since(start), 0, fmt.Errorf("create TTS HTTP client: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return time.Since(start), 0, fmt.Errorf("send TTS request: %w", err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return time.Since(start), 0, fmt.Errorf("read TTS response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return time.Since(start), 0, fmt.Errorf("tts API error (%d): %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	return time.Since(start), len(data), nil
}

func doctorTTSClient(ctx context.Context, cfg *config.Config, text string) (time.Duration, int, error) {
	start := time.Now()

	if err := doctorAudioAuthError(cfg.OpenAI.TTSAPIKey); err != nil {
		return time.Since(start), 0, err
	}

	stream, err := openaiaudio.NewTTSClient(
		cfg.OpenAI.TTSAPIKey,
		cfg.OpenAI.TTSAPIBaseURL,
		cfg.OpenAI.TTSModel,
		cfg.OpenAI.TTSVoice,
		cfg.OpenAI.TTSInstructions,
	).Synthesize(ctx, text)
	if err != nil {
		return time.Since(start), 0, err
	}

	defer func() {
		_ = stream.Close()
	}()

	data, err := io.ReadAll(stream)
	if err != nil {
		return time.Since(start), 0, fmt.Errorf("read synthesized audio: %w", err)
	}

	return time.Since(start), len(data), nil
}

func doctorSTTDirect(ctx context.Context, cfg *config.Config, path string) (time.Duration, int, error) {
	start := time.Now()

	model := cfg.OpenAI.STTModel
	if strings.TrimSpace(model) == "" {
		model = "whisper-1"
	}

	prompt := ""
	if strings.TrimSpace(cfg.OpenAI.STTPrompt) != "" {
		prompt = cfg.OpenAI.STTPrompt
	}

	file, err := os.Open(path)
	if err != nil {
		return time.Since(start), 0, fmt.Errorf("open audio file: %w", err)
	}

	defer func() {
		_ = file.Close()
	}()

	var body bytes.Buffer

	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return time.Since(start), 0, fmt.Errorf("create form file: %w", err)
	}

	if _, err := io.Copy(part, file); err != nil {
		return time.Since(start), 0, fmt.Errorf("copy audio file: %w", err)
	}

	for _, field := range [...][2]string{{"model", model}, {"response_format", "json"}, {"chunking_strategy", "auto"}} {
		if err := writer.WriteField(field[0], field[1]); err != nil {
			return time.Since(start), 0, fmt.Errorf("write %s: %w", field[0], err)
		}
	}

	if prompt != "" {
		if err := writer.WriteField("prompt", prompt); err != nil {
			return time.Since(start), 0, fmt.Errorf("write prompt: %w", err)
		}
	}

	if err := writer.Close(); err != nil {
		return time.Since(start), 0, fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openaiaudio.NormalizeAudioURL(cfg.OpenAI.STTAPIBaseURL, "/audio/transcriptions"), &body)
	if err != nil {
		return time.Since(start), 0, fmt.Errorf("create STT request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+cfg.OpenAI.STTAPIKey)

	httpClient, err := doctorOpenAIHTTPClient(cfg.OpenAI.STTAPIKey, 60*time.Second)
	if err != nil {
		return time.Since(start), 0, fmt.Errorf("create STT HTTP client: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return time.Since(start), 0, fmt.Errorf("send STT request: %w", err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return time.Since(start), 0, fmt.Errorf("read STT response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return time.Since(start), 0, fmt.Errorf("whisper API error (%d): %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return time.Since(start), 0, fmt.Errorf("decode STT response: %w", err)
	}

	return time.Since(start), len(strings.TrimSpace(payload.Text)), nil
}

func doctorSTTClient(ctx context.Context, cfg *config.Config, path string) (time.Duration, int, error) {
	start := time.Now()

	if err := doctorAudioAuthError(cfg.OpenAI.STTAPIKey); err != nil {
		return time.Since(start), 0, err
	}

	text, err := openaiaudio.NewWhisperClient(
		cfg.OpenAI.STTAPIKey,
		cfg.OpenAI.STTAPIBaseURL,
		cfg.OpenAI.STTModel,
		cfg.OpenAI.STTPrompt,
	).Transcribe(ctx, path)
	if err != nil {
		return time.Since(start), 0, err
	}

	return time.Since(start), len(text), nil
}

func doctorOpenAIHTTPClient(apiKey string, timeout time.Duration) (*http.Client, error) {
	if err := doctorAudioAuthError(apiKey); err != nil {
		return nil, err
	}

	client := new(http.Client)
	client.Timeout = timeout

	return client, nil
}

func doctorAudioAuthError(apiKey string) error {
	if strings.TrimSpace(apiKey) == "" {
		return errors.New("OpenAI TTS/STT requires an API key")
	}

	return nil
}

func writeDoctorProbeLine(label string, elapsed time.Duration, metricName string, metricValue int, probeErr error) error {
	if probeErr != nil {
		_, err := fmt.Fprintf(os.Stdout, "%s fail elapsed=%s err=%s\n", label, elapsed.Round(time.Millisecond), probeErr)
		if err != nil {
			return fmt.Errorf("write doctor probe output: %w", err)
		}

		return nil
	}

	_, err := fmt.Fprintf(os.Stdout, "%s ok elapsed=%s %s=%d\n", label, elapsed.Round(time.Millisecond), metricName, metricValue)
	if err != nil {
		return fmt.Errorf("write doctor probe output: %w", err)
	}

	return nil
}
