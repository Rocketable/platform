package backend

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionStoreRequiresConversationID(t *testing.T) {
	store, err := NewSessionService(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Stop(t.Context())) })

	_, err = store.ObserveEntries(t.Context(), " ", 0)
	require.EqualError(t, err, "conversation ID is required")
}

func TestExternalMCPBindingPersistsPrivateAndManagedConversations(t *testing.T) {
	store, err := NewSessionService(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Stop(t.Context())) })

	binding := ExternalMCPSessionState{Agent: "private-agent", PrivateConversationID: "external_mcp:private", ManagedConversationID: "slack-thread:C1:2.2", SlackChannel: "#ops"}
	require.NoError(t, store.UpsertExternalMCPSession("deploy-42", &binding))

	got, ok, err := store.ExternalMCPSession("deploy-42")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, binding, got)

	for _, conversationID := range []string{binding.PrivateConversationID, binding.ManagedConversationID} {
		externalConversationID, found, foundOK, err := store.ExternalMCPSessionByConversationID(conversationID)
		require.NoError(t, err)
		require.True(t, foundOK)
		assert.Equal(t, "deploy-42", externalConversationID)
		assert.Equal(t, binding, found)
	}
}

func TestExternalMCPConversationRegistrationIsAtomic(t *testing.T) {
	store, err := NewSessionService(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Stop(t.Context())) })

	require.NoError(t, store.RegisterExternalMCPConversation("existing", "managed-agent", &ExternalMCPSessionState{Agent: "private-agent", PrivateConversationID: "external_mcp:private-1", ManagedConversationID: "slack-thread:C1:1.1", SlackChannel: "#ops"}))
	thread, ok, err := store.Thread("slack-thread:C1:1.1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "managed-agent", thread.Agent)

	err = store.RegisterExternalMCPConversation("existing", "other-managed", &ExternalMCPSessionState{Agent: "other-private", PrivateConversationID: "external_mcp:private-2", ManagedConversationID: "slack-thread:C1:2.2", SlackChannel: "#ops"})
	require.Error(t, err)

	_, ok, err = store.Thread("slack-thread:C1:2.2")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestExternalMCPConversationCleanupRemovesOnlyBoundSessions(t *testing.T) {
	store, err := NewSessionService(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Stop(t.Context())) })

	session := ExternalMCPSessionState{Agent: "private-agent", PrivateConversationID: "external_mcp:private", ManagedConversationID: "slack-thread:C1:1.1", SlackChannel: "#ops"}
	require.NoError(t, store.RegisterExternalMCPConversation("public", "managed-agent", &session))

	for _, conversationID := range []string{session.PrivateConversationID, session.ManagedConversationID, "unrelated"} {
		_, err := store.AppendEntryID(t.Context(), conversationID, testSessionEntry(conversationID, "assistant"))
		require.NoError(t, err)
	}

	require.NoError(t, store.UpsertThread("unrelated", ThreadState{Agent: "other"}))

	require.NoError(t, store.RemoveExternalMCPConversation("public"))

	_, ok, err := store.ExternalMCPSession("public")
	require.NoError(t, err)
	assert.False(t, ok)
	_, ok, err = store.Thread(session.ManagedConversationID)
	require.NoError(t, err)
	assert.False(t, ok)

	for _, conversationID := range []string{session.PrivateConversationID, session.ManagedConversationID} {
		entries, err := store.ObserveEntries(t.Context(), conversationID, 0)
		require.NoError(t, err)
		assert.Empty(t, entries)
	}

	entries, err := store.ObserveEntries(t.Context(), "unrelated", 0)
	require.NoError(t, err)
	assert.Len(t, entries, 1)

	_, ok, err = store.Thread("unrelated")
	require.NoError(t, err)
	assert.True(t, ok)
}
