package backend

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

type OpenCodeMention struct {
	MessageID string   `json:"messageID"`
	IDs       []string `json:"ids"`
	Signature string   `json:"signature,omitempty"`
}

func (m *OpenCodeMention) Sign(key, conversationID, sender string) string {
	payload, _ := json.Marshal(struct {
		ConversationID string
		Sender         string
		MessageID      string
		IDs            []string
	}{conversationID, sender, m.MessageID, m.IDs})
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write(payload)

	return hex.EncodeToString(mac.Sum(nil))
}

func (p *openCodeSender) trustedMention(key, conversationID string) *protocol.InboundMessage {
	var mention OpenCodeMention
	if len(p.Metadata.Mention) == 0 || json.Unmarshal(p.Metadata.Mention, &mention) != nil || len(mention.IDs) == 0 || mention.MessageID != p.ID ||
		!hmac.Equal([]byte(mention.Signature), []byte(mention.Sign(key, conversationID, p.Metadata.Username))) {
		return nil
	}

	return &protocol.InboundMessage{
		Source: protocol.SourceWeb, Human: true, ConversationID: conversationID,
		Metadata: map[string]string{
			protocol.InboundPrincipalEmailMetadataKey: p.Metadata.Username,
			protocol.InboundRawTextMetadataKey:        p.Text,
			protocol.InboundWebMentionIDsMetadataKey:  strings.Join(mention.IDs, ","),
		},
	}
}

func (m *threadBridgeManager) sendWebMentions(ctx context.Context, inbound *protocol.InboundMessage) {
	ids := inbound.Metadata[protocol.InboundWebMentionIDsMetadataKey]
	if ids == "" || inbound.Source != protocol.SourceWeb || !inbound.Human {
		return
	}

	sender := inbound.Metadata[protocol.InboundPrincipalEmailMetadataKey]
	text := inbound.Metadata[protocol.InboundRawTextMetadataKey]

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	m.mu.Lock()
	directory := m.slackLookups
	m.mu.Unlock()

	for _, id := range slices.Compact(slices.Sorted(strings.SplitSeq(ids, ","))) {
		if ctx.Err() != nil {
			break
		}

		if !strings.Contains(text, "<@"+id+">") {
			continue
		}

		recipient, err := directory.SessionMentionRecipient(ctx, id)
		if err != nil {
			m.log.Error("look up session mention recipient", "error", err)
			continue
		}

		if recipient == "" {
			continue
		}

		eligible, err := m.store.MarkSharedMention(ctx, inbound.ConversationID, sender, recipient)
		if err != nil {
			m.log.Error("mark shared session mention", "error", err)
			continue
		}

		if eligible {
			if err := directory.SendSessionMention(ctx, id, inbound.ConversationID); err != nil {
				m.log.Error("send shared session mention", "error", err)
			}
		}
	}
}
