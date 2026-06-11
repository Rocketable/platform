package rocketcode

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"path/filepath"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"golang.org/x/net/html"
)

const maxAttachmentBytes = 5 * 1024 * 1024

// PromptInputRole identifies the instruction hierarchy role for a prompt input.
type PromptInputRole string

const (
	// PromptInputRoleUser identifies ordinary user prompt input.
	PromptInputRoleUser PromptInputRole = "user"
	// PromptInputRoleDeveloper identifies developer instruction prompt input.
	PromptInputRoleDeveloper PromptInputRole = "developer"
)

// PromptInput is one prompt plus optional model-visible attachments.
type PromptInput struct {
	// Role defaults to PromptInputRoleUser when empty.
	Role        PromptInputRole `json:"role,omitempty"`
	Text        string          `json:"text"`
	Attachments []Attachment    `json:"attachments,omitempty"`
	// Responses receives user-visible response items for this prompt. The runtime
	// closes it after the prompt's turn reaches a terminal state.
	Responses chan<- ChatResponse `json:"-"`
}

// Attachment is a model-visible file attachment encoded as a URL, usually a data URL.
type Attachment struct {
	MIME     string `json:"mime"`
	Filename string `json:"filename,omitempty"`
	URL      string `json:"url"`
}

// ToolResult is the output of a tool call, including optional model-visible attachments.
type ToolResult struct {
	Output      string
	Attachments []Attachment
}

// TextToolResult returns a text-only tool result.
func TextToolResult(output string) ToolResult {
	return ToolResult{Output: output}
}

func attachmentFromBytes(filename, mimeType string, data []byte) (Attachment, error) {
	if len(data) > maxAttachmentBytes {
		return Attachment{}, errors.New("attachment too large (exceeds 5MB limit)")
	}

	mimeType = normalizeMIME(mimeType)
	if !isSupportedAttachmentMIME(mimeType) {
		return Attachment{}, fmt.Errorf("unsupported attachment MIME type: %s", mimeType)
	}

	return Attachment{
		MIME:     mimeType,
		Filename: filename,
		URL:      "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data),
	}, nil
}

func normalizeMIME(mimeType string) string {
	mediaType, _, err := mime.ParseMediaType(mimeType)
	if err == nil {
		mimeType = mediaType
	}

	return strings.ToLower(strings.TrimSpace(mimeType))
}

func isSupportedAttachmentMIME(mimeType string) bool {
	return isImageAttachmentMIME(mimeType) || mimeType == "application/pdf"
}

func isImageAttachmentMIME(mimeType string) bool {
	mimeType = normalizeMIME(mimeType)
	return strings.HasPrefix(mimeType, "image/") && mimeType != "image/svg+xml" && mimeType != "image/vnd.fastbidsheet"
}

func sniffAttachmentMIME(data []byte, fallback string) string {
	switch {
	case bytes.HasPrefix(data, []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}):
		return "image/png"
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		return "image/jpeg"
	case bytes.HasPrefix(data, []byte{0x47, 0x49, 0x46, 0x38}):
		return "image/gif"
	case bytes.HasPrefix(data, []byte{0x42, 0x4d}):
		return "image/bmp"
	case bytes.HasPrefix(data, []byte("%PDF-")):
		return "application/pdf"
	case len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	}

	return normalizeMIME(fallback)
}

func mimeFromFilename(filename string) string {
	if ext := filepath.Ext(filename); ext != "" {
		if mimeType := mime.TypeByExtension(ext); mimeType != "" {
			return normalizeMIME(mimeType)
		}
	}

	return "application/octet-stream"
}

func promptInputMessage(input PromptInput) responses.ResponseInputItemUnionParam {
	role := promptInputMessageRole(input.Role)
	if len(input.Attachments) == 0 {
		return inputMessageParam(role, easyInputStringContent(input.Text))
	}

	content := responses.ResponseInputMessageContentListParam{}
	if input.Text != "" {
		content = append(content, responses.ResponseInputContentUnionParam{OfInputText: &responses.ResponseInputTextParam{Text: input.Text}})
	}

	content = appendAttachmentContent(content, input.Attachments...)

	return inputMessageParam(role, easyInputListContent(content))
}

func inputMessageParam(role responses.EasyInputMessageRole, content responses.EasyInputMessageContentUnionParam) responses.ResponseInputItemUnionParam {
	return responses.ResponseInputItemUnionParam{OfMessage: &responses.EasyInputMessageParam{
		Content: content,
		Role:    role,
		Type:    "message",
	}}
}

func easyInputStringContent(text string) responses.EasyInputMessageContentUnionParam {
	return responses.EasyInputMessageContentUnionParam{OfString: openai.String(text)}
}

func easyInputListContent(items responses.ResponseInputMessageContentListParam) responses.EasyInputMessageContentUnionParam {
	return responses.EasyInputMessageContentUnionParam{OfInputItemContentList: items}
}

func promptInputMessageRole(role PromptInputRole) responses.EasyInputMessageRole {
	if role == PromptInputRoleDeveloper {
		return responses.EasyInputMessageRoleDeveloper
	}

	return responses.EasyInputMessageRoleUser
}

func appendAttachmentContent(content responses.ResponseInputMessageContentListParam, attachments ...Attachment) responses.ResponseInputMessageContentListParam {
	for _, attachment := range attachments {
		mimeType := normalizeMIME(attachment.MIME)
		switch {
		case isImageAttachmentMIME(mimeType):
			content = append(content, responses.ResponseInputContentUnionParam{OfInputImage: &responses.ResponseInputImageParam{Detail: responses.ResponseInputImageDetailAuto, ImageURL: openai.String(attachment.URL)}})
		case mimeType == "application/pdf":
			content = append(content, responses.ResponseInputContentUnionParam{OfInputFile: &responses.ResponseInputFileParam{Filename: openai.String(attachment.Filename), FileData: openai.String(attachment.URL)}})
		}
	}

	return content
}

func functionCallOutputContent(result ToolResult) responses.ResponseFunctionCallOutputItemListParam {
	content := responses.ResponseFunctionCallOutputItemListParam{{OfInputText: &responses.ResponseInputTextContentParam{Text: result.Output}}}

	for _, attachment := range result.Attachments {
		mimeType := normalizeMIME(attachment.MIME)
		switch {
		case isImageAttachmentMIME(mimeType):
			content = append(content, responses.ResponseFunctionCallOutputItemUnionParam{OfInputImage: &responses.ResponseInputImageContentParam{Detail: responses.ResponseInputImageContentDetailAuto, ImageURL: openai.String(attachment.URL)}})
		case mimeType == "application/pdf":
			content = append(content, responses.ResponseFunctionCallOutputItemUnionParam{OfInputFile: &responses.ResponseInputFileContentParam{Filename: openai.String(attachment.Filename), FileData: openai.String(attachment.URL)}})
		}
	}

	return content
}

func attachmentOutputMessage(result ToolResult) string {
	if len(result.Attachments) == 0 {
		return result.Output
	}

	parts := []string{result.Output}
	for _, attachment := range result.Attachments {
		label := attachment.Filename
		if label == "" {
			label = attachment.MIME
		}

		parts = append(parts, fmt.Sprintf("Attached %s (%s)", label, attachment.MIME))
	}

	return strings.Join(parts, "\n")
}

func textFromHTMLString(s string) (string, error) {
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return "", fmt.Errorf("parse HTML: %w", err)
	}

	var (
		out  strings.Builder
		walk func(*html.Node, bool)
	)

	walk = func(n *html.Node, skip bool) {
		if n.Type == html.ElementNode {
			switch strings.ToLower(n.Data) {
			case "script", "style", "noscript", "iframe", "object", "embed":
				skip = true
			}
		}

		if n.Type == html.TextNode && !skip {
			text := strings.TrimSpace(n.Data)
			if text != "" {
				if out.Len() > 0 {
					out.WriteByte(' ')
				}

				out.WriteString(text)
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, skip)
		}
	}
	walk(doc, false)

	return strings.TrimSpace(out.String()), nil
}

func originForURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}

	return u.Scheme + "://" + u.Host
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read limited body: %w", err)
	}

	if int64(len(data)) > limit {
		return nil, errors.New("response too large (exceeds 5MB limit)")
	}

	return data, nil
}
