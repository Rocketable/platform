package harness

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"golang.org/x/net/html"
)

const maxAttachmentBytes = 5 * 1024 * 1024

type PromptInputRole string

const (
	PromptInputRoleUser      PromptInputRole = "user"
	PromptInputRoleDeveloper PromptInputRole = "developer"
)

type PromptInput struct {
	ID                  string
	TurnID              string
	Role                PromptInputRole
	Texts               []string
	ExpandShellCommands bool `json:",omitempty"`
	Header              string
	Attachments         []Attachment
	DirectSkill         *PromptInputDirectSkill
	Agent               string
	Responses           chan<- ChatResponse `json:"-"`
}

type PromptInputDirectSkill struct {
	Name           string `json:"name"`
	Arguments      string `json:"arguments,omitempty"`
	PlainIfUnknown bool   `json:"plainIfUnknown,omitempty"`
}

type Attachment struct {
	MIME     string `json:"mime"`
	Filename string `json:"filename,omitempty"`
	URL      string `json:"url"`
}

type ToolResult struct {
	Output      string
	Attachments []Attachment
	Data        any
}

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

func PromptInputMessage(input *PromptInput) responses.ResponseInputItemUnionParam {
	role := responses.EasyInputMessageRoleUser
	if input.Role == PromptInputRoleDeveloper {
		role = responses.EasyInputMessageRoleDeveloper
	}

	content := easyInputStringContent(strings.Join(input.Texts, ""))
	if len(input.Texts) > 1 || len(input.Attachments) > 0 {
		parts := responses.ResponseInputMessageContentListParam{}

		for _, text := range input.Texts {
			if text != "" {
				parts = append(parts, responses.ResponseInputContentUnionParam{OfInputText: &responses.ResponseInputTextParam{Text: text}})
			}
		}

		content = responses.EasyInputMessageContentUnionParam{OfInputItemContentList: appendAttachmentContent(parts, input.Attachments...)}
	}

	message := inputMessageParam(role, content)

	extra := map[string]any{}
	if input.Header != "" {
		extra["prompt_header"] = input.Header
	}

	if input.ID != "" {
		extra["input_id"] = input.ID
	}

	if len(extra) > 0 {
		message.OfMessage.SetExtraFields(extra)
	}

	return message
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
