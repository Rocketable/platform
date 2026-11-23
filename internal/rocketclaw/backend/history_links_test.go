package backend

import (
	"fmt"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/require"
)

type historyLink struct {
	entry, parent int64
	length        int
}

func chatHistoryLinks(t *testing.T, s *SessionService, conversationID string) []historyLink {
	t.Helper()

	links, err := queryRows(t.Context(), s.db, `SELECT entry_id, COALESCE(parent_entry_id, 0), COALESCE(parent_length, -1) FROM history_links WHERE conversation_id = $1 ORDER BY seq`, "history links", func(row rowScanner) (historyLink, error) {
		var link historyLink
		return link, row.Scan(&link.entry, &link.parent, &link.length)
	}, conversationID)
	require.NoError(t, err)

	return links
}

func TestRunEntriesJoinTheChatPathWhereTheyArrive(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	recordTestConversations(t, s, "main", map[string]ConversationKind{"chat": KindWebChat, "run": KindExternalMCPRun, "other": KindWebChat, "continuing": KindWebChat, "cron": KindCronRun})

	_, err := s.AppendEntryID(ctx, "chat", testSessionEntry("first", "one"))
	require.NoError(t, err)
	finishTestRunTurn(t, s, "run", "chat", "run-1", testSessionEntry("report", "two"))
	_, err = s.AppendEntryID(ctx, "chat", testSessionEntry("second", "three"))
	require.NoError(t, err)

	require.Equal(t, []string{"message:first", "message:one", "message:report", "message:two", "message:second", "message:three"}, observedEntryTexts(t, s, "chat"))
	require.Equal(t, []string{"message:report", "message:two"}, observedEntryTexts(t, s, "run"), "the run's own history is unchanged")

	_, _, err = (stateDAO{db: s.db}).bindProducer(ctx, "cron", "other")
	require.NoError(t, err)
	_, err = s.AppendEntryID(ctx, "cron", testSessionEntry("job", "earlier"))
	require.NoError(t, err)
	_, err = (&Runtime{Sessions: s}).ContinueRun(ctx, "cron", "continuing", false)
	require.NoError(t, err)
	_, err = s.AppendEntryID(ctx, "cron", testSessionEntry("late", "later"))
	require.NoError(t, err)

	for _, chat := range []string{"other", "continuing"} {
		require.Equal(t, []string{"message:job", "message:earlier", "message:late", "message:later"}, observedEntryTexts(t, s, chat), "a run that keeps working still reaches "+chat)
	}
}

func TestDeletedRunEntriesKeepTheChatPath(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	recordTestConversations(t, s, "main", map[string]ConversationKind{"chat": KindWebChat, "run": KindExternalMCPRun})

	_, err := s.AppendEntryID(ctx, "chat", testSessionEntry("first", "one"))
	require.NoError(t, err)
	finishTestRunTurn(t, s, "run", "chat", "run-1", testSessionEntry("report", "two"))
	_, err = s.AppendEntryID(ctx, "chat", testSessionEntry("second", "three"))
	require.NoError(t, err)

	deleted, err := s.DeleteSession(ctx, "run")
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	require.Empty(t, chatHistoryLinks(t, s, "run"), "the run's own links go with its entries")

	require.Equal(t, []string{"message:first", "message:one", "message:second", "message:three"}, observedEntryTexts(t, s, "chat"), "the chat keeps its history before the deleted report")

	nodes, err := s.MessageTree(ctx, "chat")
	require.NoError(t, err)

	ids, parents := make([]string, 0, len(nodes)), make([]string, 0, len(nodes))

	for _, node := range nodes {
		require.True(t, node.OnPath, node.Text)

		ids, parents = append(ids, node.ID), append(parents, node.Parent)
	}

	require.Len(t, ids, 4)
	require.Equal(t, append([]string{""}, ids[:3]...), parents, "the tree keeps one line through the deleted report")
}

func TestBoundRunEntriesFollowTheChatLeaf(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	recordTestConversations(t, s, "main", map[string]ConversationKind{"chat": KindWebChat, "run": KindCronRun})

	job, err := s.AppendEntryID(ctx, "run", testSessionEntry("job", "done"))
	require.NoError(t, err)
	hello, err := s.AppendEntryID(ctx, "chat", testSessionEntry("hello", "hi"))
	require.NoError(t, err)
	_, _, err = (stateDAO{db: s.db}).bindRun(ctx, "run", "chat")
	require.NoError(t, err)

	require.NoError(t, startTurnDB(ctx, s.db, "next-turn", "chat", protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "next", true)))

	var anchor int64
	require.NoError(t, queryScalar(ctx, s.db, "turn anchor", `SELECT history_anchor_id FROM turns WHERE id = 'next-turn'`, &anchor))
	require.Equal(t, job, anchor, "a turn continues from the bound run's last entry")

	next, err := s.AppendEntryID(ctx, "chat", testSessionEntry("next", "reply"))
	require.NoError(t, err)

	require.Equal(t, []historyLink{{hello, 0, -1}, {job, hello, -1}, {next, job, -1}}, chatHistoryLinks(t, s, "chat"))
	require.Equal(t, []string{"message:job", "message:done", "message:hello", "message:hi", "message:next", "message:reply"}, observedEntryTexts(t, s, "chat"))
}

func TestAppendAtTheCurrentPoint(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()

	for _, tc := range []struct {
		chat     string
		entry    int
		boundary int
		length   int
		want     []string
	}{
		{"inside-an-entry", 0, 2, 2, []string{"message:a", "message:b", "message:new", "message:reply"}},
		{"before-an-entry", 1, 0, -1, []string{"message:a", "message:b", "message:c", "message:d", "message:new", "message:reply"}},
	} {
		recordTestConversations(t, s, "main", map[string]ConversationKind{tc.chat: KindWebChat})

		first, err := s.AppendEntryID(ctx, tc.chat, testReplayEntry(`{"type":"message","role":"user","content":"a"}`, `{"type":"message","role":"assistant","content":"b"}`,
			`{"type":"message","role":"user","content":"c"}`, `{"type":"message","role":"assistant","content":"d"}`))
		require.NoError(t, err)
		second, err := s.AppendEntryID(ctx, tc.chat, testSessionEntry("e", "f"))
		require.NoError(t, err)

		_, _, err = stageRevertDB(ctx, s.db, tc.chat, fmt.Sprintf("%d:%d", []int64{first, second}[tc.entry], tc.boundary), protocol.RevertBefore)
		require.NoError(t, err)

		appended, err := s.AppendEntryID(ctx, tc.chat, testSessionEntry("new", "reply"))
		require.NoError(t, err)

		marker, _, _, err := s.RevertState(ctx, tc.chat)
		require.NoError(t, err)
		require.Empty(t, marker, tc.chat)

		require.Equal(t, []historyLink{{first, 0, -1}, {second, first, -1}, {appended, first, tc.length}}, chatHistoryLinks(t, s, tc.chat), "the new entry continues from the current point")
		require.Equal(t, tc.want, observedEntryTexts(t, s, tc.chat), tc.chat)
	}
}
