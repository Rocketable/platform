package harness

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

type mcpResult struct {
	tool, text string
	content    *starlark.List
	structured starlark.Value
}

func newMCPResult(tool, text string, result *mcp.CallToolResult) (*mcpResult, error) {
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

	return &mcpResult{tool: tool, text: text, content: list, structured: structured}, nil
}

func (m *mcpResult) String() string       { return m.text }
func (m *mcpResult) Text() string         { return m.text }
func (m *mcpResult) Type() string         { return "mcp_result" }
func (m *mcpResult) Freeze()              {}
func (m *mcpResult) Truth() starlark.Bool { return m.text != "" || m.content.Len() > 0 }
func (m *mcpResult) Ok() bool             { return true }

func (m *mcpResult) Len() int                   { return len(m.text) }
func (m *mcpResult) Index(i int) starlark.Value { return codemode.TextIndex(m, i) }
func (m *mcpResult) Slice(start, end, step int) starlark.Value {
	return codemode.TextSlice(m, start, end, step)
}

func (m *mcpResult) Binary(op syntax.Token, y starlark.Value, side starlark.Side) (starlark.Value, error) {
	z, err := codemode.TextBinary(m, op, y, side)
	if err != nil {
		return nil, fmt.Errorf("%w", err)
	}

	return z, nil
}

func (m *mcpResult) Hash() (uint32, error) {
	return 0, errors.New("unhashable type: mcp_result")
}

func (m *mcpResult) MarshalJSON() ([]byte, error) {
	encoded, _ := codemode.NewText(m.text, codemode.PlainText{}).MarshalJSON()

	return encoded, nil
}

func (m *mcpResult) Attr(name string) (starlark.Value, error) {
	if v, ok := codemode.ResultAttr(m, nil, name); ok {
		return v, nil
	}

	server, tool, _ := strings.Cut(m.tool, ".")

	switch name {
	case "server":
		return starlark.String(server), nil
	case "tool":
		return starlark.String(tool), nil
	case "text":
		return starlark.String(m.text), nil
	case "content":
		return m.content, nil
	case "structured":
		return m.structured, nil
	}

	method, _ := starlark.String(m.text).Attr(name)

	return method, nil
}

func (m *mcpResult) AttrNames() []string {
	return codemode.ResultAttrNames(append(starlark.String("").AttrNames(), "content", "server", "structured", "text", "tool")...)
}

var (
	_ codemode.Result    = (*mcpResult)(nil)
	_ codemode.Text      = (*mcpResult)(nil)
	_ starlark.HasAttrs  = (*mcpResult)(nil)
	_ starlark.Sliceable = (*mcpResult)(nil)
	_ starlark.HasBinary = (*mcpResult)(nil)
)
