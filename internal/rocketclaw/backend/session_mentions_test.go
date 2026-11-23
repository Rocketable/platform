package backend

import (
	"encoding/json"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeMentionUsesOriginalMessageAndSender(t *testing.T) {
	const key, session, username = "test-key", "opencode:box:ses_a", "sender@example.test"

	mention := OpenCodeMention{MessageID: "msg_a", IDs: []string{"U123"}}
	mention.Signature = mention.Sign(key, session, username)
	payload, err := json.Marshal(mention)
	require.NoError(t, err)

	prompt := openCodeSender{ID: "msg_a", Text: "See <@U123>"}
	prompt.Metadata.Username, prompt.Metadata.Mention = username, payload
	inbound := prompt.trustedMention(key, session)
	require.NotNil(t, inbound)
	require.Equal(t, "U123", inbound.Metadata[protocol.InboundWebMentionIDsMetadataKey])
	require.Nil(t, prompt.trustedMention(key, "opencode:box:ses_b"))
	prompt.ID = "msg_copy"
	require.Nil(t, prompt.trustedMention(key, session))
	prompt.ID, prompt.Metadata.Username = "msg_a", "someone-else@example.test"
	require.Nil(t, prompt.trustedMention(key, session))
}

func TestSharedMentionMark(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	store := newTestSessionService(t)
	recordTestConversations(t, store, "main", map[string]ConversationKind{"chat": KindWebChat})
	_, err := store.recordOpenCodeSession(ctx, "test", &openCodeSession{ID: "chat", Title: "Example"}, "main", "")
	require.NoError(t, err)
	saved, err := store.SaveTabState(ctx, "reader@example.test", &TabState{}, 0)
	require.NoError(t, err)
	require.True(t, saved)

	for _, test := range []struct{ id, path string }{
		{id: "chat", path: "/s/Y2hhdA"},
		{id: "opencode:test:chat", path: "/oc/b3BlbmNvZGU6dGVzdDpjaGF0"},
	} {
		accepted, err := store.MarkSharedMention(ctx, test.id, "sender@example.test", "reader@example.test")
		require.NoError(t, err)
		require.False(t, accepted, "a private tab cannot be mentioned")

		require.NoError(t, store.ShareTab(ctx, "main", test.path, nil))
		accepted, err = store.MarkSharedMention(ctx, test.id, "sender@example.test", "unknown@example.test")
		require.NoError(t, err)
		require.False(t, accepted, "an unknown web login cannot be notified")

		accepted, err = store.MarkSharedMention(ctx, test.id, "sender@example.test", "reader@example.test")
		require.NoError(t, err)
		require.True(t, accepted)

		require.Equal(t, "new note=Mentioned you", attentionOf(t, store, "reader@example.test", test.path))
		markTab(t, store.db, "reader@example.test", test.path, turnEnd)
		require.Equal(t, "new note=Mentioned you", attentionOf(t, store, "reader@example.test", test.path))
		_, _, err = store.PollAttention(ctx, "reader@example.test", test.path, false)
		require.NoError(t, err)
		markTab(t, store.db, "reader@example.test", test.path, turnEnd)
		require.Equal(t, "read", attentionOf(t, store, "reader@example.test", test.path))
	}
}
