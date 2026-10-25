package rpc

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPJSONCompressionAllocations(t *testing.T) {
	result := testing.Benchmark(BenchmarkHTTPJSONCompression)
	// Allow pool misses, including race-mode discards, but not a compressor per response.
	require.Less(t, result.AllocedBytesPerOp(), int64(512<<10))
}

func TestHTTPJSONCompression(t *testing.T) {
	var writers sync.Pool

	for _, text := range []string{"", "alpha", "quote\" 日本語"} {
		t.Run(text, func(t *testing.T) {
			t.Parallel()

			data := bytes.Repeat([]byte(text), 256)

			for _, encoding := range []string{"", "gzip"} {
				request := httptest.NewRequest(http.MethodPost, "/api/Example", http.NoBody)
				request.Header.Set("Accept-Encoding", encoding)

				for range 2 {
					response := httptest.NewRecorder()
					httpJSON(response, request, data, &writers)
					require.Equal(t, "application/json", response.Header().Get("Content-Type"))
					require.Equal(t, "Accept-Encoding", response.Header().Get("Vary"))
					require.Equal(t, encoding, response.Header().Get("Content-Encoding"))

					if encoding == "" {
						require.Equal(t, string(data), response.Body.String())
						continue
					}

					reader, err := gzip.NewReader(response.Body)
					require.NoError(t, err)
					unpacked, err := io.ReadAll(reader)
					require.NoError(t, err)
					require.NoError(t, reader.Close())
					require.Equal(t, data, unpacked)
				}
			}
		})
	}
}

func BenchmarkHTTPJSONCompression(b *testing.B) {
	var writers sync.Pool

	data := bytes.Repeat([]byte(`{"message":"hello"}`), 256)
	request := httptest.NewRequest(http.MethodPost, "/api/Example", http.NoBody)
	request.Header.Set("Accept-Encoding", "gzip")
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()

	for b.Loop() {
		httpJSON(httptest.NewRecorder(), request, data, &writers)
	}
}
