package harness

import (
	"cmp"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
)

func newMCPResult(tool, text string, result *mcp.CallToolResult) (starlark.Value, error) {
	content := make([]starlark.Value, 0, len(result.Content))
	files := map[string]int{}
	file := func(kind string, block int, data []byte, mimeType string) starlark.Value {
		files[kind]++

		mimeType, upright, err := protocol.StraightenImage(sniffAttachmentMIME(data, mimeType), string(data))
		if err != nil {
			return codemode.NewFailure(tool, err.Error(), nil)
		}

		name := fmt.Sprintf("%s-%d", kind, files[kind])

		if _, ext, _ := strings.Cut(mimeType, "/"); ext != "" {
			ext, _, _ = strings.Cut(strings.TrimPrefix(ext, "x-"), "+")
			name += "." + ext
		}

		return codemode.NewFile(codemode.FileSpec{Name: name, Mime: mimeType, Content: upright, Source: fmt.Sprintf("mcp %s block %d", tool, block)})
	}
	resource := func(uri, mimeType, text string) *starlark.Dict {
		dict := starlark.NewDict(3)
		_ = dict.SetKey(starlark.String("uri"), starlark.String(uri))
		_ = dict.SetKey(starlark.String("mime"), starlark.String(mimeType))

		if text != "" {
			_ = dict.SetKey(starlark.String("text"), starlark.String(text))
		}

		return dict
	}

	for i, block := range result.Content {
		switch block := block.(type) {
		case *mcp.TextContent:
			content = append(content, starlark.String(block.Text))
		case *mcp.ImageContent:
			content = append(content, file("image", i+1, block.Data, block.MIMEType))
		case *mcp.AudioContent:
			content = append(content, file("audio", i+1, block.Data, block.MIMEType))
		case *mcp.EmbeddedResource:
			embedded := cmp.Or(block.Resource, &mcp.ResourceContents{})
			content = append(content, resource(embedded.URI, embedded.MIMEType, embedded.Text))
		case *mcp.ResourceLink:
			content = append(content, resource(block.URI, block.MIMEType, ""))
		}
	}

	structured := starlark.Value(starlark.None)

	if result.StructuredContent != nil {
		raw, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return nil, fmt.Errorf("marshal structured content: %w", err)
		}

		structured, err = starlark.Call(&starlark.Thread{Name: tool}, starjson.Module.Members["decode"], starlark.Tuple{starlark.String(raw)}, nil)
		if err != nil {
			return nil, fmt.Errorf("decode structured content: %w", err)
		}
	}

	list := starlark.NewList(content)
	list.Freeze()
	structured.Freeze()

	server, name, _ := strings.Cut(tool, ".")
	attrs := starlark.StringDict{"server": starlark.String(server), "tool": starlark.String(name), "text": starlark.String(text), "content": list, "structured": structured}

	return codemode.NewText("mcp_result", text, attrs, nil), nil
}
