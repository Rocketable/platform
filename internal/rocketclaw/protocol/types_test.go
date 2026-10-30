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

	content := InboundContent{Attachments: []InboundAttachment{
		{Name: "one.png", MIMEType: "image/png", Data: []byte("one")},
		{Name: "two.png", MIMEType: "image/png", Data: []byte("two")},
		{Name: "nil.png", MIMEType: "image/png"},
		{Name: "empty.png", MIMEType: "image/png", Data: []byte{}},
	}}
	// Fourteen entries expose Clone's spare capacity without Clip.
	content.Attachments = append(content.Attachments, make([]InboundAttachment, 10)...)
	inbound := NewInboundMessageFromContent(SourceSlack, InboundKindPrompt, &content, true)

	require.Len(t, inbound.Attachments, len(content.Attachments))
	require.Equal(t, content.Attachments[:3], inbound.Attachments[:3])
	require.Equal(t, InboundAttachment{Name: "empty.png", MIMEType: "image/png"}, inbound.Attachments[3])
	require.Equal(t, len(inbound.Attachments), cap(inbound.Attachments))

	inbound.Attachments[0].Name = "changed.png"
	inbound.Attachments[0].MIMEType = "image/jpeg"
	inbound.Attachments[0].Data[0] = 'X'
	require.Equal(t, InboundAttachment{Name: "one.png", MIMEType: "image/png", Data: []byte("one")}, content.Attachments[0])

	content.Attachments[1].Name = "changed.png"
	content.Attachments[1].Data[0] = 'Y'
	require.Equal(t, InboundAttachment{Name: "two.png", MIMEType: "image/png", Data: []byte("two")}, inbound.Attachments[1])
}
