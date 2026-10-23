package backend

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExternalMCPOriginDetailsStayOutsideHistory(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	binding := ExternalMCPSessionState{PrivateConversationID: "private", ManagedConversationID: "managed", Agent: "helper", SlackChannel: "#triage", OriginPairs: map[string]string{"topic": "billing"}}
	require.NoError(t, store.RegisterExternalMCPConversation("external", "helper", &binding))

	for _, id := range []string{"private", "managed"} {
		_, got, found, err := store.ExternalMCPSessionByConversationID(id)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, binding, got)
		entries, err := store.ObserveEntries(t.Context(), id)
		require.NoError(t, err)
		require.Empty(t, entries)
	}
}

func TestChatOriginFactsReportsFailures(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()

	for _, id := range []string{"first", "second"} {
		require.NoError(t, store.UpsertThread(id, ThreadState{Agent: "main"}))
	}

	seen := 0

	for _, err := range store.ChatOriginFacts(ctx, "") {
		require.NoError(t, err)

		seen++

		break
	}

	require.Equal(t, 1, seen, "stopping early ends the scan")

	_, err := store.db.ExecContext(ctx, `UPDATE conversations SET external_id = 'external', external_run = 'private', external_agent = 'helper', external_channel = '#triage',
    origin_pairs = '"not an object"' WHERE conversation_id = 'first'`)
	require.NoError(t, err)

	var errs []error

	for _, err := range store.ChatOriginFacts(ctx, "") {
		if err != nil {
			errs = append(errs, err)
		}
	}

	require.Len(t, errs, 1)
	require.ErrorContains(t, errs[0], "decode external MCP origin pairs")
	// A single-session origin read must neither visit nor decode unrelated bindings.
	seen = 0

	for facts, err := range store.ChatOriginFacts(ctx, "second") {
		require.NoError(t, err)
		require.Equal(t, "second", facts.ConversationID)

		seen++
	}

	require.Equal(t, 1, seen)

	for _, err := range store.ChatOriginFacts(ctx, "missing") {
		require.NoError(t, err)
		t.Fatal("missing origin unexpectedly returned a row")
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()

	errs = nil

	for _, err := range store.ChatOriginFacts(canceled, "") {
		errs = append(errs, err)
	}

	require.Len(t, errs, 1)
	require.ErrorIs(t, errs[0], context.Canceled)
}
