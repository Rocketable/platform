package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/openaiaudio"
	"github.com/stretchr/testify/require"
)

func TestRunDoctorReportsRocketCodeRuntime(t *testing.T) {
	workspace := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(cwd)) })
	t.Setenv("PATH", "")

	require.NoError(t, os.WriteFile(filepath.Join(workspace, defaultConfigPath), []byte(`{
		"workspace": ".",
		"openai": {"api_key": "test-key"},
		"mcp_external": {"enabled": true, "listen_addr": "127.0.0.1:8765"}
	}`), 0o600))

	output := captureStdout(t, func() error { return runDoctor(nil) })
	require.Contains(t, output, "RocketCode: OK (library)")
}

func TestRunDoctorReportsLegacyConfigAndWorkDir(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)

	require.NoError(t, os.WriteFile(filepath.Join(workspace, defaultConfigPath), []byte(`{
		"workspace": ".",
		"openai": {"api_key": "rocket-key"},
		"mcp_external": {"enabled": true, "listen_addr": "127.0.0.1:8765"}
	}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, legacyConfigPath), []byte(`{
		"workspace": ".",
		"openai": {"api_key": "legacy-key"},
		"mcp_external": {"enabled": true, "listen_addr": "127.0.0.1:8765"}
	}`), 0o600))

	output := captureStdout(t, func() error { return runDoctor(nil) })
	require.Contains(t, output, "Configuration: OK (femtoclaw.json)")
	require.Contains(t, output, "Work directory: .femtoclaw")
}

func TestRunDoctorReportsRocketConfigWorkDir(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)

	require.NoError(t, os.WriteFile(filepath.Join(workspace, defaultConfigPath), []byte(`{
		"workspace": ".",
		"openai": {"api_key": "test-key"},
		"mcp_external": {"enabled": true, "listen_addr": "127.0.0.1:8765"}
	}`), 0o600))

	output := captureStdout(t, func() error { return runDoctor(nil) })
	require.Contains(t, output, "Configuration: OK (rocketclaw.json)")
	require.Contains(t, output, "Work directory: .rocketclaw")
}

func TestRunDoctorAudioValidatesArgumentsBeforeConfigLoad(t *testing.T) {
	require.ErrorContains(t, runDoctor([]string{"tts", "extra"}), "does not accept positional")
	require.ErrorContains(t, runDoctor([]string{"stt"}), "requires -file")
}

func TestRunDoctorAudioRejectsBadFlagsBeforeConfigLoad(t *testing.T) {
	require.ErrorContains(t, runDoctor([]string{"tts", "--bad"}), "parse doctor tts flags")
	require.ErrorContains(t, runDoctor([]string{"stt", "--bad"}), "parse doctor stt flags")
}

func TestRunDoctorRejectsBadFlagBeforeConfigLoad(t *testing.T) {
	require.ErrorContains(t, runDoctor([]string{"--bad"}), "parse doctor flags")
}

func TestRunDoctorReportsConfigLoadError(t *testing.T) {
	workspace := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(cwd)) })

	require.ErrorContains(t, runDoctor(nil), "load config")
}

func TestRunDoctorReportsOutputWriteError(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, defaultConfigPath), []byte(`{
		"workspace": ".",
		"openai": {"api_key": "test-key"},
		"mcp_external": {"enabled": true, "listen_addr": "127.0.0.1:8765"}
	}`), 0o600))

	closeStdoutForTest(t)

	err := runDoctor(nil)
	require.ErrorContains(t, err, "write doctor output")
}

func TestRunDoctorAudioReportsProbeWriteErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "audio unavailable", http.StatusInternalServerError)
	}))
	defer server.Close()

	t.Run("tts", func(t *testing.T) {
		doctorAudioWorkspace(t, server.URL)
		closeStdoutForTest(t)

		err := runDoctor([]string{"tts", "-text", "hello"})
		require.ErrorContains(t, err, "write doctor probe output")
	})

	t.Run("stt", func(t *testing.T) {
		workspace := doctorAudioWorkspace(t, server.URL)
		audioPath := filepath.Join(workspace, "sample.wav")
		require.NoError(t, os.WriteFile(audioPath, []byte("audio"), 0o600))
		closeStdoutForTest(t)

		err := runDoctor([]string{"stt", "-file", audioPath})
		require.ErrorContains(t, err, "write doctor probe output")
	})
}

func TestDoctorSTTDirectReportsMissingAudioFile(t *testing.T) {
	cfg := &config.Config{OpenAI: config.OpenAIConfig{STTAPIKey: "test-key"}}
	_, _, err := doctorSTTDirect(t.Context(), cfg, filepath.Join(t.TempDir(), "missing.wav"))
	require.ErrorContains(t, err, "open audio file")

	_, _, err = doctorSTTDirect(t.Context(), cfg, t.TempDir())
	require.ErrorContains(t, err, "copy audio file")
}

func TestDoctorDirectReportsRequestErrors(t *testing.T) {
	cfg := &config.Config{OpenAI: config.OpenAIConfig{
		STTAPIKey:     "test-key",
		STTAPIBaseURL: "http://[::1",
		TTSAPIKey:     "test-key",
		TTSAPIBaseURL: "http://[::1",
	}}

	_, bytes, err := doctorTTSDirect(t.Context(), cfg, "hello")
	require.Zero(t, bytes)
	require.ErrorContains(t, err, "create TTS request")

	audioPath := filepath.Join(t.TempDir(), "sample.wav")
	require.NoError(t, os.WriteFile(audioPath, []byte("audio"), 0o600))

	_, textLen, err := doctorSTTDirect(t.Context(), cfg, audioPath)
	require.Zero(t, textLen)
	require.ErrorContains(t, err, "create STT request")
}

func TestDoctorAudioReportsMissingAPIKey(t *testing.T) {
	cfg := &config.Config{}

	_, bytes, err := doctorTTSDirect(t.Context(), cfg, "hello")
	require.Zero(t, bytes)
	require.ErrorContains(t, err, "create TTS HTTP client")
	require.ErrorContains(t, err, "requires an API key")

	audioPath := filepath.Join(t.TempDir(), "sample.wav")
	require.NoError(t, os.WriteFile(audioPath, []byte("audio"), 0o600))
	_, textLen, err := doctorSTTDirect(t.Context(), cfg, audioPath)
	require.Zero(t, textLen)
	require.ErrorContains(t, err, "create STT HTTP client")
	require.ErrorContains(t, err, "requires an API key")

	_, bytes, err = doctorTTSClient(t.Context(), cfg, "hello")
	require.Zero(t, bytes)
	require.ErrorContains(t, err, "requires an API key")

	_, textLen, err = doctorSTTClient(t.Context(), cfg, audioPath)
	require.Zero(t, textLen)
	require.ErrorContains(t, err, "requires an API key")
}

func TestRunDoctorTTSSucceedsAgainstLocalServer(t *testing.T) {
	requests := make(chan map[string]string, 2)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			t.Errorf("TTS request method = %q; want %q", req.Method, http.MethodPost)
		}

		if req.URL.Path != "/audio/speech" {
			t.Errorf("TTS request path = %q; want %q", req.URL.Path, "/audio/speech")
		}

		if got := req.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("TTS authorization header = %q; want %q", got, "Bearer test-key")
		}

		if got := req.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("TTS content type = %q; want %q", got, "application/json")
		}

		var payload map[string]string
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Errorf("decode TTS request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)

			return
		}

		requests <- payload

		if _, err := io.WriteString(w, "opus"); err != nil {
			t.Errorf("write TTS response: %v", err)
		}
	}))
	defer server.Close()

	doctorAudioWorkspace(t, server.URL)

	output := captureStdout(t, func() error {
		return runDoctor([]string{"tts", "-text", "hello"})
	})
	require.Contains(t, output, "direct ok")
	require.Contains(t, output, "client ok")
	require.Contains(t, output, "bytes=4")

	for range 2 {
		payload := <-requests
		require.Equal(t, "tts-1", payload["model"])
		require.Equal(t, "alloy", payload["voice"])
		require.Equal(t, "hello", payload["input"])
		require.Equal(t, "opus", payload["response_format"])
		require.Equal(t, "speak clearly", payload["instructions"])
	}
}

func TestRunDoctorSTTSucceedsAgainstLocalServer(t *testing.T) {
	type sttRequest struct {
		fields   map[string]string
		fileName string
		fileData string
	}

	requests := make(chan sttRequest, 2)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			t.Errorf("STT request method = %q; want %q", req.Method, http.MethodPost)
		}

		if req.URL.Path != "/audio/transcriptions" {
			t.Errorf("STT request path = %q; want %q", req.URL.Path, "/audio/transcriptions")
		}

		if got := req.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("STT authorization header = %q; want %q", got, "Bearer test-key")
		}

		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse STT multipart form: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)

			return
		}

		file, header, err := req.FormFile("file")
		if err != nil {
			t.Errorf("STT form file error: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)

			return
		}

		defer func() {
			if err := file.Close(); err != nil {
				t.Errorf("close STT form file: %v", err)
			}
		}()

		data, err := io.ReadAll(file)
		if err != nil {
			t.Errorf("read STT form file: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)

			return
		}

		requests <- sttRequest{
			fields: map[string]string{
				"model":             req.FormValue("model"),
				"response_format":   req.FormValue("response_format"),
				"chunking_strategy": req.FormValue("chunking_strategy"),
				"prompt":            req.FormValue("prompt"),
			},
			fileName: header.Filename,
			fileData: string(data),
		}

		w.Header().Set("Content-Type", "application/json")

		if _, err := io.WriteString(w, `{"text":" hello "}`); err != nil {
			t.Errorf("write STT response: %v", err)
		}
	}))
	defer server.Close()

	workspace := doctorAudioWorkspace(t, server.URL)
	audioPath := filepath.Join(workspace, "sample.wav")
	require.NoError(t, os.WriteFile(audioPath, []byte("audio"), 0o600))

	output := captureStdout(t, func() error {
		return runDoctor([]string{"stt", "-file", audioPath})
	})
	require.Contains(t, output, "direct ok")
	require.Contains(t, output, "client ok")
	require.Contains(t, output, "text_len=5")

	for range 2 {
		request := <-requests
		require.Equal(t, "sample.wav", request.fileName)
		require.Equal(t, "audio", request.fileData)
		require.Equal(t, "whisper-1", request.fields["model"])
		require.Equal(t, "json", request.fields["response_format"])
		require.Equal(t, "auto", request.fields["chunking_strategy"])
		require.Equal(t, "domain words", request.fields["prompt"])
	}
}

func TestDoctorAudioDirectDefaultsOmitBlankPromptFields(t *testing.T) {
	ttsRequests := make(chan map[string]string, 1)
	sttRequests := make(chan map[string]string, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/audio/speech":
			var payload map[string]string
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				t.Errorf("decode TTS request: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)

				return
			}

			ttsRequests <- payload

			if _, err := io.WriteString(w, "opus"); err != nil {
				t.Errorf("write TTS response: %v", err)
			}
		case "/audio/transcriptions":
			if err := req.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("parse STT multipart form: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)

				return
			}

			fields := map[string]string{
				"model":             req.FormValue("model"),
				"response_format":   req.FormValue("response_format"),
				"chunking_strategy": req.FormValue("chunking_strategy"),
				"prompt":            req.FormValue("prompt"),
			}
			if _, ok := req.MultipartForm.Value["prompt"]; ok {
				fields["prompt_present"] = "true"
			}

			sttRequests <- fields

			w.Header().Set("Content-Type", "application/json")

			if _, err := io.WriteString(w, `{"text":" hello "}`); err != nil {
				t.Errorf("write STT response: %v", err)
			}
		default:
			t.Errorf("audio request path = %q", req.URL.Path)
			http.NotFound(w, req)
		}
	}))
	defer server.Close()

	cfg := &config.Config{OpenAI: config.OpenAIConfig{
		STTAPIKey:       "test-key",
		STTAPIBaseURL:   server.URL,
		STTPrompt:       " \t\n",
		TTSAPIKey:       "test-key",
		TTSAPIBaseURL:   server.URL,
		TTSInstructions: " \t\n",
	}}

	_, bytes, err := doctorTTSDirect(t.Context(), cfg, "hello")
	require.NoError(t, err)
	require.Equal(t, 4, bytes)

	ttsPayload := <-ttsRequests
	require.Equal(t, "tts-1", ttsPayload["model"])
	require.Equal(t, "alloy", ttsPayload["voice"])
	require.Equal(t, "hello", ttsPayload["input"])
	require.Equal(t, "opus", ttsPayload["response_format"])
	require.NotContains(t, ttsPayload, "instructions")

	audioPath := filepath.Join(t.TempDir(), "sample.wav")
	require.NoError(t, os.WriteFile(audioPath, []byte("audio"), 0o600))

	_, textLen, err := doctorSTTDirect(t.Context(), cfg, audioPath)
	require.NoError(t, err)
	require.Equal(t, 5, textLen)

	sttPayload := <-sttRequests
	require.Equal(t, "whisper-1", sttPayload["model"])
	require.Equal(t, "json", sttPayload["response_format"])
	require.Equal(t, "auto", sttPayload["chunking_strategy"])
	require.Empty(t, sttPayload["prompt"])
	require.Empty(t, sttPayload["prompt_present"])
}

func TestRunDoctorAudioReportsProbeFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "audio unavailable", http.StatusInternalServerError)
	}))
	defer server.Close()

	t.Run("tts", func(t *testing.T) {
		doctorAudioWorkspace(t, server.URL)

		var errRun error

		output := captureStdout(t, func() error {
			errRun = runDoctor([]string{"tts", "-text", "hello"})

			return nil
		})

		require.Error(t, errRun)
		require.Equal(t, 1, exitCodeForError(errRun))
		require.Contains(t, output, "direct fail")
		require.Contains(t, output, "client fail")
		require.Contains(t, output, "tts API error (500): audio unavailable")
	})

	t.Run("stt", func(t *testing.T) {
		workspace := doctorAudioWorkspace(t, server.URL)
		audioPath := filepath.Join(workspace, "sample.wav")
		require.NoError(t, os.WriteFile(audioPath, []byte("audio"), 0o600))

		var errRun error

		output := captureStdout(t, func() error {
			errRun = runDoctor([]string{"stt", "-file", audioPath})

			return nil
		})

		require.Error(t, errRun)
		require.Equal(t, 1, exitCodeForError(errRun))
		require.Contains(t, output, "direct fail")
		require.Contains(t, output, "client fail")
		require.Contains(t, output, "whisper API error (500): audio unavailable")
	})
}

func TestDoctorAudioHelpers(t *testing.T) {
	tests := map[string]string{
		"":                              "https://api.openai.com/v1/audio/speech",
		"https://api.openai.com":        "https://api.openai.com/v1/audio/speech",
		"https://api.openai.com/v1":     "https://api.openai.com/v1/audio/speech",
		"https://api.openai.com/custom": "https://api.openai.com/v1/custom/audio/speech",
		"https://example.com/openai":    "https://example.com/openai/audio/speech",
		"://bad":                        "://bad/audio/speech",
	}

	for base, want := range tests {
		require.Equal(t, want, openaiaudio.NormalizeAudioURL(base, "/audio/speech"))
	}

	require.ErrorContains(t, doctorAudioAuthError(" "), "requires an API key")
	require.NoError(t, doctorAudioAuthError("test-key"))

	client, err := doctorOpenAIHTTPClient(" ", time.Second)
	require.Nil(t, client)
	require.ErrorContains(t, err, "requires an API key")
	client, err = doctorOpenAIHTTPClient("test-key", 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, 3*time.Second, client.Timeout)

	output := captureStdout(t, func() error {
		return writeDoctorProbeLine("direct", time.Second, "bytes", 12, nil)
	})
	require.Contains(t, output, "direct ok")
	require.Contains(t, output, "bytes=12")

	output = captureStdout(t, func() error {
		return writeDoctorProbeLine("direct", time.Second, "bytes", 0, os.ErrPermission)
	})
	require.Contains(t, output, "direct fail")
	require.Contains(t, output, os.ErrPermission.Error())
}

func TestWriteDoctorProbeLineReportsWriteErrors(t *testing.T) {
	for _, tt := range []struct {
		name     string
		errProbe error
	}{
		{name: "success line"},
		{name: "failure line", errProbe: os.ErrPermission},
	} {
		t.Run(tt.name, func(t *testing.T) {
			closeStdoutForTest(t)

			err := writeDoctorProbeLine("direct", time.Second, "bytes", 12, tt.errProbe)
			require.ErrorContains(t, err, "write doctor probe output")
		})
	}
}

func closeStdoutForTest(t *testing.T) {
	t.Helper()

	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })

	oldStdout := os.Stdout
	os.Stdout = writer

	t.Cleanup(func() { os.Stdout = oldStdout })

	require.NoError(t, writer.Close())
}

func doctorAudioWorkspace(t *testing.T, serverURL string) string {
	t.Helper()

	workspace := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(cwd)) })

	require.NoError(t, os.WriteFile(filepath.Join(workspace, defaultConfigPath), fmt.Appendf(nil, `{
		"workspace": ".",
		"openai": {
			"api_key": "test-key",
			"stt_base_url": %q,
			"stt_prompt": "domain words",
			"tts_base_url": %q,
			"tts_instructions": "speak clearly"
		},
		"mcp_external": {"enabled": true, "listen_addr": "127.0.0.1:8765"}
	}`, serverURL, serverURL), 0o600))

	return workspace
}
