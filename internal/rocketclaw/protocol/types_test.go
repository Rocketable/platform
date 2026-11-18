package protocol

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInboundAttachmentCopy(t *testing.T) {
	for _, empty := range [][]InboundAttachment{nil, {}} {
		content := InboundContent{Attachments: empty}
		inbound := NewInboundMessageFromContent(SourceSlack, InboundKindPrompt, &content, true)
		require.Nil(t, inbound.Attachments, "both empty inputs keep the same nil output")
		require.Zero(t, inbound.AttachmentPresence)
	}

	image := []byte("\x89PNG\r\n\x1a\none")
	content := InboundContent{Attachments: []InboundAttachment{
		{Name: "one.png", MIMEType: "image/png", Data: image},
		{Name: "two.png", MIMEType: "image/png", Data: []byte("two")},
		{Name: "empty.png", MIMEType: "image/png", Data: []byte{}},
	}}
	inbound := NewInboundMessageFromContent(SourceSlack, InboundKindPrompt, &content, true)

	normalized := []InboundAttachment{{Name: "one.png", MIMEType: "image/png", Data: image}}
	warnings := []string{"Skipped attachment two.png because text/plain is not supported.", "Skipped attachment empty.png because it was empty."}

	require.Equal(t, normalized, inbound.Attachments, "only what the model accepts enters")
	require.Equal(t, warnings, inbound.AttachmentWarnings)
	require.Equal(t, AttachmentPresenceImages, inbound.AttachmentPresence)
	require.Equal(t, len(inbound.Attachments), cap(inbound.Attachments))
	require.Equal(t, normalized, content.Attachments, "the content kept for later is the normalized one")
	require.Equal(t, warnings, content.AttachmentWarnings)

	again := NewInboundMessageFromContent(SourceSlack, InboundKindPrompt, &content, true)
	require.Equal(t, []any{normalized, warnings}, []any{again.Attachments, again.AttachmentWarnings}, "building again from normalized content changes nothing")

	inbound.Attachments[0].Data[0] = 'X'
	require.Equal(t, normalized, content.Attachments, "the message owns its bytes")
}
