package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"sync"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func TestExecuteReturnsTypedMCPResults(t *testing.T) {
	var picture bytes.Buffer
	require.NoError(t, png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 2, 3))))

	wave := []byte("RIFF\x24\x00\x00\x00WAVEfmt ")

	var (
		mu    sync.Mutex
		calls []string
	)

	url := startRocketCodeMCPHTTPServer(t, func(server *mcp.Server) {
		add := func(name string, result *mcp.CallToolResult) {
			server.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				mu.Lock()
				defer mu.Unlock()

				calls = append(calls, name)

				return result, nil
			})
		}
		add("mixed", &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: "see "},
			&mcp.ImageContent{Data: picture.Bytes(), MIMEType: "image/png"},
			&mcp.AudioContent{Data: wave, MIMEType: "audio/x-wav"},
			&mcp.ImageContent{Data: picture.Bytes(), MIMEType: "image/jpeg"},
			&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///a.txt", MIMEType: "text/plain", Text: "alpha"}},
			&mcp.ResourceLink{URI: "https://example.com/y.pdf", Name: "y", MIMEType: "application/pdf"},
			&mcp.TextContent{Text: "done"},
		}})
		add("structured", &mcp.CallToolResult{StructuredContent: map[string]any{"n": 1, "tags": []string{"a"}}})
		add("fail", &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "nope"}}})
	})

	var permissions PermissionSet
	require.NoError(t, permissions.Set("mcp", "demo.*", PermissionAllow))

	reg := mcpclient.New(t.TempDir(), map[string]mcpclient.ServerConfig{"demo": {URL: url}}, nil)
	run := (&toolFactory{mcpRegistry: reg}).mcpToolsFor(&Agent{Permission: permissions}, nil)[executeToolName]
	journal := recordingJournal()
	execute := func() string {
		t.Helper()

		looper := &looper{Journal: journal, observations: &turnObservations{journal: journal, turnID: "turn-1"}, Permissions: permissions}
		ctx := context.WithValue(t.Context(), toolCallContextKey{}, toolCallContext{looper: looper, observations: looper.observations, callID: "call-1"})

		raw, err := json.Marshal(map[string]string{"code": `def main():
    m = demo_mixed()
    s = demo_structured()
    e = demo_fail()
    img, audio = m.content[1], m.content[2]
    return {
        "mixed": [type(m), m.ok, str(m), bool(m), m.text, m == "see done", m.structured, m.upper(), json.encode(m)],
        "types": ",".join([type(c) for c in m.content]),
        "texts": [m.content[0], m.content[6]],
        "image": [img.name, img.mime, img.size, img.width, img.height, img.source, img.bytes() == m.content[3].bytes()],
        "second_image": [m.content[3].name, m.content[3].mime],
        "audio": [audio.name, audio.mime, audio.size, audio.width, audio.source],
        "resources": [m.content[4], m.content[5]],
        "structured": [s.text, s.structured, json.decode(s)["n"], s.content],
        "failure": [type(e), e.ok, e.error, bool(e), e.or_else("fallback")],
        "file_argument": demo_structured(payload=img).error,
    }
`})
		require.NoError(t, err)

		result, err := run.Call(ctx, raw, nil, emptyToolCallMetadata())
		require.NoError(t, err)

		return result.Output
	}

	first := execute()
	require.JSONEq(t, fmt.Sprintf(`{
		"mixed": ["mcp_result", true, "see done", true, "see done", true, null, "SEE DONE", "\"see done\""],
		"types": "string,file,file,file,dict,dict,string",
		"texts": ["see ", "done"],
		"image": ["image-1.png", "image/png", %d, 2, 3, "mcp demo.mixed block 2", true],
		"second_image": ["image-2.png", "image/png"],
		"audio": ["audio-1.wav", "audio/x-wav", 16, null, "mcp demo.mixed block 3"],
		"resources": [{"mime": "text/plain", "text": "alpha", "uri": "file:///a.txt"}, {"mime": "application/pdf", "uri": "https://example.com/y.pdf"}],
		"structured": ["{\"n\":1,\"tags\":[\"a\"]}", {"n": 1, "tags": ["a"]}, 1, []],
		"failure": ["error", false, "demo.fail: nope", false, "fallback"],
		"file_argument": "demo.structured: argument payload got a file object; pass image-1.base64(), a path, or text"
	}`, picture.Len()), first)
	require.Equal(t, []string{"mixed", "structured", "fail"}, calls, "a file argument never reaches the server")

	calls = nil

	require.Equal(t, first, execute(), "a replayed script rebuilds text, files, resources, structured content and failures from the journal")
	require.Empty(t, calls, "MCP calls are not repeated on replay")
}
