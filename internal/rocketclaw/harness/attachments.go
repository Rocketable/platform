package harness

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"mime"
	"slices"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"golang.org/x/net/html"
)

type PromptInputRole string

const PromptInputRoleUser PromptInputRole = "user"

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
	case len(data) >= 18 && bytes.HasPrefix(data, []byte("BM")) && slices.Contains([]uint32{12, 40, 52, 56, 64, 108, 124}, binary.LittleEndian.Uint32(data[14:])) &&
		binary.LittleEndian.Uint32(data[10:]) >= 14+binary.LittleEndian.Uint32(data[14:]) && binary.LittleEndian.Uint32(data[2:]) >= binary.LittleEndian.Uint32(data[10:]):
		return "image/bmp"
	case bytes.HasPrefix(data, []byte("%PDF-")):
		return "application/pdf"
	case len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	}

	return normalizeMIME(fallback)
}

func PromptInputMessage(input *PromptInput) responses.ResponseInputItemUnionParam {
	var content responses.EasyInputMessageContentUnionParam

	if len(input.Texts) > 1 || len(input.Attachments) > 0 {
		parts := responses.ResponseInputMessageContentListParam{}

		for _, text := range input.Texts {
			if text != "" {
				parts = append(parts, responses.ResponseInputContentUnionParam{OfInputText: &responses.ResponseInputTextParam{Text: text}})
			}
		}

		for _, attachment := range input.Attachments {
			mimeType := normalizeMIME(attachment.MIME)
			switch {
			case isImageAttachmentMIME(mimeType):
				parts = append(parts, responses.ResponseInputContentUnionParam{OfInputImage: withImageName(responses.ResponseInputImageParam{Detail: responses.ResponseInputImageDetailAuto, ImageURL: openai.String(attachment.URL)}, attachment.Filename)})
			case mimeType == "application/pdf":
				parts = append(parts, responses.ResponseInputContentUnionParam{OfInputFile: &responses.ResponseInputFileParam{Filename: openai.String(attachment.Filename), FileData: openai.String(attachment.URL)}})
			}
		}

		content = responses.EasyInputMessageContentUnionParam{OfInputItemContentList: parts}
	} else {
		content = easyInputStringContent(strings.Join(input.Texts, ""))
	}

	message := inputMessageParam(responses.EasyInputMessageRoleUser, content)

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

func textFromHTMLString(s string) (string, error) {
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return "", fmt.Errorf("parse HTML: %w", err)
	}

	var (
		out  strings.Builder
		walk func(*html.Node)
	)

	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch strings.ToLower(n.Data) {
			case "script", "style", "noscript", "iframe", "object", "embed":
				return
			}
		}

		if n.Type == html.TextNode {
			text := strings.TrimSpace(n.Data)
			if text != "" {
				if out.Len() > 0 {
					out.WriteByte(' ')
				}

				out.WriteString(text)
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	return strings.TrimSpace(out.String()), nil
}
