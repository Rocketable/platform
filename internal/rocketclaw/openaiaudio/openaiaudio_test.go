package openaiaudio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeOggOpusEmitsAudioPackets(t *testing.T) {
	stream := bytes.Join([][]byte{
		oggPage([]byte("OpusHead v1")),
		oggPage([]byte("OpusTags encoder")),
		oggPage([]byte("frame-one")),
		oggPage(append(bytes.Repeat([]byte("a"), 255), []byte("frame-two")...)),
	}, nil)

	var frames [][]byte

	err := DecodeOggOpus(bytes.NewReader(stream), func(frame []byte) error {
		frames = append(frames, frame)

		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, [][]byte{[]byte("frame-one"), append(bytes.Repeat([]byte("a"), 255), []byte("frame-two")...)}, frames)
}

func TestDecodeOggOpusRejectsInvalidMagic(t *testing.T) {
	err := DecodeOggOpus(bytes.NewReader([]byte("not an ogg page header here")), func([]byte) error { return nil })
	require.EqualError(t, err, "invalid ogg magic string")
}

func TestDecodeOggOpusReportsTruncatedPages(t *testing.T) {
	header := make([]byte, 27)
	copy(header, "OggS")
	header[26] = 1

	tests := []struct {
		name    string
		data    []byte
		wantErr string
	}{
		{name: "segment table", data: header, wantErr: "read segment table"},
		{name: "segment data", data: append(append([]byte(nil), header...), 4), wantErr: "read segment data"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := DecodeOggOpus(bytes.NewReader(tt.data), func([]byte) error { return nil })
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestDecodeOggOpusReturnsCallbackError(t *testing.T) {
	errFrame := errors.New("frame rejected")
	err := DecodeOggOpus(bytes.NewReader(oggPage([]byte("frame"))), func([]byte) error { return errFrame })
	require.ErrorIs(t, err, errFrame)
}

func oggPage(packets ...[]byte) []byte {
	var (
		body   []byte
		lacing []byte
	)

	for _, packet := range packets {
		for len(packet) >= 255 {
			lacing = append(lacing, 255)
			body = append(body, packet[:255]...)
			packet = packet[255:]
		}

		lacing = append(lacing, byte(len(packet)))
		body = append(body, packet...)
	}

	header := make([]byte, 27, 27+len(lacing)+len(body))
	copy(header, "OggS")
	header[26] = byte(len(lacing))

	return append(append(header, lacing...), body...)
}

func TestWhisperClientTranscribeUsesConfiguredModel(t *testing.T) {
	var gotAuth, gotPath string

	gotFields := map[string]string{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path

		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if !assert.NoError(t, err) {
			return
		}

		assert.Equal(t, "multipart/form-data", mediaType)

		reader := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}

			if !assert.NoError(t, err) {
				return
			}

			name := part.FormName()
			if name == "model" || name == "chunking_strategy" || name == "prompt" {
				data, err := io.ReadAll(part)
				if !assert.NoError(t, err) {
					return
				}

				gotFields[name] = string(data)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"hello from whisper"}`))
	}))
	defer server.Close()

	tmp := t.TempDir()
	path := filepath.Join(tmp, "clip.ogg")
	require.NoError(t, os.WriteFile(path, []byte("audio-data"), 0o644))

	client := NewWhisperClient("secret", server.URL, "whisper-1", "Prefer product names exactly as spoken.")
	text, err := client.Transcribe(context.Background(), path)
	require.NoError(t, err)
	assert.Equal(t, "hello from whisper", text)
	assert.Equal(t, "Bearer secret", gotAuth)
	assert.Equal(t, "auto", gotFields["chunking_strategy"])
	assert.Equal(t, "whisper-1", gotFields["model"])
	assert.Equal(t, "Prefer product names exactly as spoken.", gotFields["prompt"])
	assert.Equal(t, "/audio/transcriptions", gotPath)
}

func TestWhisperClientTranscribeOmitsWhitespaceOnlyPrompt(t *testing.T) {
	gotFields := map[string]string{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if !assert.NoError(t, err) {
			return
		}

		assert.Equal(t, "multipart/form-data", mediaType)

		reader := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}

			if !assert.NoError(t, err) {
				return
			}

			name := part.FormName()
			if name == "model" || name == "chunking_strategy" || name == "prompt" {
				data, err := io.ReadAll(part)
				if !assert.NoError(t, err) {
					return
				}

				gotFields[name] = string(data)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"hello from whisper"}`))
	}))
	defer server.Close()

	tmp := t.TempDir()
	path := filepath.Join(tmp, "clip.ogg")
	require.NoError(t, os.WriteFile(path, []byte("audio-data"), 0o644))

	client := NewWhisperClient("secret", server.URL, "whisper-1", "   ")
	_, err := client.Transcribe(context.Background(), path)
	require.NoError(t, err)

	_, ok := gotFields["prompt"]
	assert.False(t, ok)
}

func TestNormalizeAudioURL(t *testing.T) {
	tests := []struct {
		name   string
		base   string
		suffix string
		want   string
	}{
		{name: "empty base", suffix: "/audio/transcriptions", want: "https://api.openai.com/v1/audio/transcriptions"},
		{name: "openai host", base: "https://api.openai.com", suffix: "/audio/transcriptions", want: "https://api.openai.com/v1/audio/transcriptions"},
		{name: "openai v1", base: "https://api.openai.com/v1", suffix: "/audio/speech", want: "https://api.openai.com/v1/audio/speech"},
		{name: "openai non v1 path", base: "https://api.openai.com/audio", suffix: "/transcriptions", want: "https://api.openai.com/v1/audio/transcriptions"},
		{name: "custom host", base: "https://proxy.example/openai", suffix: "/audio/speech", want: "https://proxy.example/openai/audio/speech"},
		{name: "custom host already suffixed", base: "https://proxy.example/openai/audio/speech", suffix: "/audio/speech", want: "https://proxy.example/openai/audio/speech"},
		{name: "fallback string", base: "not a url/", suffix: "/audio/speech", want: "not a url/audio/speech"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, NormalizeAudioURL(tt.base, tt.suffix))
		})
	}
}

func TestTTSClientSynthesizeUsesConfiguredFields(t *testing.T) {
	var (
		gotAuth string
		gotBody struct {
			Model          string `json:"model"`
			Voice          string `json:"voice"`
			Instructions   string `json:"instructions"`
			ResponseFormat string `json:"response_format"`
		}
		gotPath string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path

		body, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			return
		}

		if !assert.NoError(t, json.Unmarshal(body, &gotBody)) {
			return
		}

		_, _ = w.Write([]byte("opus-data"))
	}))
	defer server.Close()

	client := NewTTSClient("secret", server.URL, "tts-1", "alloy", "Speak in a calm, concise tone.")
	stream, err := client.Synthesize(context.Background(), "hello world")
	require.NoError(t, err)
	t.Cleanup(func() {
		assert.NoError(t, stream.Close())
	})

	data, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.Equal(t, "opus-data", string(data))
	assert.Equal(t, "Bearer secret", gotAuth)
	assert.Equal(t, "/audio/speech", gotPath)
	assert.Equal(t, "tts-1", gotBody.Model)
	assert.Equal(t, "alloy", gotBody.Voice)
	assert.Equal(t, "Speak in a calm, concise tone.", gotBody.Instructions)
	assert.Equal(t, "opus", gotBody.ResponseFormat)
}

func TestTTSClientSynthesizeOmitsWhitespaceOnlyInstructions(t *testing.T) {
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			return
		}

		if !assert.NoError(t, json.Unmarshal(body, &gotBody)) {
			return
		}

		_, _ = w.Write([]byte("opus-data"))
	}))
	defer server.Close()

	client := NewTTSClient("secret", server.URL, "tts-1", "alloy", " \t ")
	stream, err := client.Synthesize(context.Background(), "hello world")
	require.NoError(t, err)
	t.Cleanup(func() {
		assert.NoError(t, stream.Close())
	})

	_, err = io.ReadAll(stream)
	require.NoError(t, err)

	_, ok := gotBody["instructions"]
	assert.False(t, ok)
}

func TestTTSClientSynthesizeFormatOverridesResponseFormat(t *testing.T) {
	var gotBody struct {
		ResponseFormat string `json:"response_format"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			return
		}

		if !assert.NoError(t, json.Unmarshal(body, &gotBody)) {
			return
		}

		_, _ = w.Write([]byte("mp3-data"))
	}))
	defer server.Close()

	client := NewTTSClient("secret", server.URL, "tts-1", "alloy", "")
	stream, err := client.SynthesizeFormat(context.Background(), "hello world", "mp3")
	require.NoError(t, err)
	t.Cleanup(func() {
		assert.NoError(t, stream.Close())
	})

	_, err = io.ReadAll(stream)
	require.NoError(t, err)
	assert.Equal(t, "mp3", gotBody.ResponseFormat)
}

func TestTTSClientSynthesizeFormatDefaultsAndReportsAPIError(t *testing.T) {
	var gotBody struct {
		Model          string `json:"model"`
		Voice          string `json:"voice"`
		ResponseFormat string `json:"response_format"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			return
		}

		if !assert.NoError(t, json.Unmarshal(body, &gotBody)) {
			return
		}

		http.Error(w, "quota exhausted", http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := NewTTSClient("secret", server.URL, "", "", "")
	stream, err := client.SynthesizeFormat(context.Background(), "hello world", " \t ")
	require.Nil(t, stream)
	require.EqualError(t, err, "tts API error (429): quota exhausted")
	assert.Equal(t, "tts-1", gotBody.Model)
	assert.Equal(t, "alloy", gotBody.Voice)
	assert.Equal(t, "opus", gotBody.ResponseFormat)
}

func TestTTSClientSynthesizeReportsRequestErrors(t *testing.T) {
	client := NewTTSClient("secret", "http://[::1", "", "", "")

	stream, err := client.Synthesize(context.Background(), "hello world")
	require.Nil(t, stream)
	require.ErrorContains(t, err, "create TTS request")
}

func TestTTSClientSynthesizeReportsTransportErrors(t *testing.T) {
	errTransport := errors.New("transport down")
	client := NewTTSClient("secret", "https://example.test", "", "", "")
	client.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errTransport
	})}

	stream, err := client.Synthesize(context.Background(), "hello world")
	require.Nil(t, stream)
	require.ErrorIs(t, err, errTransport)
	require.ErrorContains(t, err, "send TTS request")
}

func TestWhisperClientTranscribeMissingFile(t *testing.T) {
	client := NewWhisperClient("secret", "https://example.test", "", "")
	_, err := client.Transcribe(context.Background(), filepath.Join(t.TempDir(), "missing.ogg"))
	require.ErrorContains(t, err, "open audio file")
}

func TestWhisperClientTranscribeReportsAudioReadErrors(t *testing.T) {
	client := NewWhisperClient("secret", "https://example.test", "", "")
	_, err := client.Transcribe(context.Background(), t.TempDir())
	require.ErrorContains(t, err, "copy audio file")
}

func TestWhisperClientTranscribeReportsRequestErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clip.ogg")
	require.NoError(t, os.WriteFile(path, []byte("audio-data"), 0o644))

	client := NewWhisperClient("secret", "http://[::1", "", "")

	_, err := client.Transcribe(context.Background(), path)
	require.ErrorContains(t, err, "create request")
}

func TestWhisperClientTranscribeReportsTransportErrors(t *testing.T) {
	errTransport := errors.New("transport down")
	path := filepath.Join(t.TempDir(), "clip.ogg")
	require.NoError(t, os.WriteFile(path, []byte("audio-data"), 0o644))

	client := NewWhisperClient("secret", "https://example.test", "", "")
	client.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errTransport
	})}

	_, err := client.Transcribe(context.Background(), path)
	require.ErrorIs(t, err, errTransport)
	require.ErrorContains(t, err, "send request")
}

func TestWhisperClientTranscribeReportsReadErrors(t *testing.T) {
	errAudioRead := errors.New("read failed")
	path := filepath.Join(t.TempDir(), "clip.ogg")
	require.NoError(t, os.WriteFile(path, []byte("audio-data"), 0o644))

	client := NewWhisperClient("secret", "https://example.test", "", "")
	client.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(iotest.ErrReader(errAudioRead))}, nil
	})}

	_, err := client.Transcribe(context.Background(), path)
	require.ErrorIs(t, err, errAudioRead)
	require.ErrorContains(t, err, "read response")
}

func TestWhisperClientTranscribeReportsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad audio", http.StatusBadRequest)
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "clip.ogg")
	require.NoError(t, os.WriteFile(path, []byte("audio-data"), 0o644))

	client := NewWhisperClient("secret", server.URL, "", "")
	_, err := client.Transcribe(context.Background(), path)
	require.EqualError(t, err, "whisper API error (400): bad audio")
}

func TestWhisperClientTranscribeRejectsInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte("not-json"))
		assert.NoError(t, err)
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "clip.ogg")
	require.NoError(t, os.WriteFile(path, []byte("audio-data"), 0o644))

	client := NewWhisperClient("secret", server.URL, "", "")
	_, err := client.Transcribe(context.Background(), path)
	require.ErrorContains(t, err, "decode response")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
