package rpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"maps"
	"net/netip"
	"os"
	"slices"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// The bulk origin facts and History's per-conversation facts must decide the same origins.
func TestChatOriginFactsMatchHistory(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	sessions, err := backend.NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1"))
	cfg := &config.Config{Workspace: t.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}
	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll(cfg.RuntimeDirName()+"/agents", 0o700))
	server := New(withoutBackgroundJobs(), sessions, config.NewLockedConfig(cfg), &mockChannels{SidebarChannelAgentChoicesFunc: func(_ context.Context, channel string) (string, []string, error) {
		return channel, []string{"main"}, nil
	}}, &mockCronJobs{LoadOneOffCronjobFunc: func(stem string) (protocol.OneOffCronjob, error) {
		return protocol.OneOffCronjob{RelativePath: "cron/" + stem + ".md"}, nil
	}})
	scheduled := "cron:cron/daily.md:20260905T010000.000000001Z:first"
	oneOff := "one-off-cron:cron/report.md:20260905T020000.000000002Z:second"
	cronSlack, plainSlack, destination := "slack-thread:C1:1.1", "slack-thread:C2:2.2", "slack-thread:C3:3.3"

	for id, thread := range map[string]backend.ThreadState{
		scheduled: {Agent: "cron-agent", CreatedBy: backend.ThreadCreatedByCron}, oneOff: {Agent: "report-agent", CreatedBy: backend.ThreadCreatedByCron},
		cronSlack: {Agent: "main", CreatedBy: backend.ThreadCreatedByCron}, plainSlack: {Agent: "main", CreatedBy: "U1"},
		destination: {Agent: "main"}, "external_mcp:private": {Agent: "producer"},
		"web:" + scheduled: {Agent: "main", CreatedBy: "alice"}, "web:" + oneOff: {Agent: "main", CreatedBy: "alice"},
		"web-cron-synced": {Agent: "main", CreatedBy: "alice"}, "web-later-cron": {Agent: "main", CreatedBy: "alice"}, "web-empty": {Agent: "main", CreatedBy: "alice"},
	} {
		require.NoError(t, sessions.UpsertThread(id, thread))
	}

	require.NoError(t, sessions.UpsertExternalMCPSession("ext-1", &backend.ExternalMCPSessionState{Agent: "producer", PrivateConversationID: "external_mcp:private", ManagedConversationID: destination, SlackChannel: "C3", OriginPairs: map[string]string{"Ticket": "T-1", "b": "two"}}))

	// entry appends a turn; a source attributes it by entry ID, as SyncConversation does, or by name.
	entry := func(id, source string, byEntryID bool) {
		entryID, err := sessions.AppendEntryID(ctx, id, &rocketcode.SessionEntry{Type: "turn"})
		require.NoError(t, err)

		provenance := `jsonb_build_object('sync_source_conversation_id', $2::text)`
		if byEntryID {
			provenance = `jsonb_build_object('sync_source_entry_id', (SELECT MIN(id) FROM session_entries WHERE conversation_id = $2))`
		}

		if source != "" {
			_, err = db.ExecContext(ctx, `UPDATE session_entries SET entry_json = (entry_json::jsonb || `+provenance+`)::json WHERE id = $1`, entryID, source)
			require.NoError(t, err)
		}
	}
	for _, step := range []struct {
		id, source string
		byEntryID  bool
	}{
		{scheduled, "", false}, {oneOff, "", false}, {"external_mcp:private", "", false},
		{cronSlack, scheduled, true}, {cronSlack, "", false},
		{plainSlack, oneOff, true},
		{destination, "external_mcp:private", true},
		{"web:" + oneOff, oneOff, true},
		{"web-cron-synced", scheduled, false}, {"web-cron-synced", "", false},
		{"web-later-cron", "", false}, {"web-later-cron", oneOff, false},
	} {
		entry(step.id, step.source, step.byEntryID)
	}

	// A reply that started before cron history synced in sorts its checkpoint ahead of every saved entry.
	_, err = db.ExecContext(ctx, `INSERT INTO active_turns (id, conversation_id, inbound_json, output_trace_json, created_at_unix_ns, updated_at_unix_ns, history_anchor_id)
VALUES ('early-reply', $1, '{}', '[]', 1, 1, 0)`, cronSlack)
	require.NoError(t, err)

	bulk := make(map[string]string)

	var originated []string

	for facts, err := range sessions.ChatOriginFacts(ctx, "") {
		require.NoError(t, err)

		var raw []byte
		if origin, _ := backend.DecideOrigin(&facts); origin != nil {
			raw, err = json.Marshal(origin)
			require.NoError(t, err)

			originated = append(originated, facts.ConversationID)
		}

		bulk[facts.ConversationID] = string(raw)
	}

	require.ElementsMatch(t, []string{cronSlack, plainSlack, destination, "web:" + scheduled, "web:" + oneOff, "web-cron-synced", "web-later-cron", "web-empty"}, slices.Collect(maps.Keys(bulk)), "cron runs and private External MCP conversations stay hidden")
	require.ElementsMatch(t, []string{cronSlack, destination, "web:" + scheduled, "web:" + oneOff, "web-cron-synced"}, originated)

	listed := make(map[string]string)

	require.NoError(t, server.listSessions(&mockServerStream{ContextFunc: func() context.Context {
		return ctx
	}, SendMsgFunc: func(message any) error {
		for _, session := range message.(*ListSessionsResponse).Sessions {
			listed[session.Id] = session.GetCronName()
			require.Equal(t, session.GetCronName() != "", session.GetCron())
		}

		return nil
	}}))
	require.Equal(t, map[string]string{cronSlack: "daily", plainSlack: "", destination: "", "web:" + oneOff: "report", "web-cron-synced": "daily", "web-later-cron": ""}, listed, "only creating cron history counts; private producers remain absent")

	for id, origin := range bulk {
		for _, limit := range []int32{0, 1} {
			history, err := server.history(ctx, &HistoryRequest{Id: id, Limit: limit})
			require.NoError(t, err)
			require.Equal(t, origin, history.Origin, "%s with limit %d", id, limit)
		}
	}
	// Isolate physical origin from projection even for an ineligible copied origin;
	// mutation denial is asserted separately by the unchanged Revert eligibility tests.
	for _, id := range []string{"web-cron-synced", "web-later-cron"} {
		_, err = db.ExecContext(ctx, `UPDATE managed_conversations SET revert_message_id = (SELECT MIN(id)::text || ':0' FROM session_entries WHERE conversation_id = $1) WHERE conversation_id = $1`, id)
		require.NoError(t, err)
		history, err := server.history(ctx, &HistoryRequest{Id: id})
		require.NoError(t, err)
		require.Equal(t, bulk[id], history.Origin, "first-message cutoff cannot erase physical origin")
		require.Equal(t, id == "web-later-cron", history.RevertEligible, "only the creating source decides pure-Web eligibility")
		_, err = db.ExecContext(ctx, `UPDATE managed_conversations SET revert_message_id = '' WHERE conversation_id = $1`, id)
		require.NoError(t, err)
	}

	search := func(ctx context.Context, query string) (map[string]string, error) {
		response, err := server.searchSessions(ctx, &SearchSessionsRequest{Query: query})
		if err != nil {
			return nil, err
		}

		matches := make(map[string]string)

		for _, match := range response.GetMatches() {
			require.Equal(t, "Origin", match.GetField(), query)
			matches[match.GetConversationId()] = match.GetText()
		}

		return matches, nil
	}
	for _, test := range []struct {
		query string
		want  map[string]string
	}{
		{" T-1 ", map[string]string{destination: "external mcp external conversation: ext-1 agent: producer ticket=t-1 b=two"}},
		{"Report-Agent", map[string]string{"web:" + oneOff: "cron source: cron/report.md stem: report run kind: one-off run id: one-off-cron:cron/report.md:20260905t020000.000000002z:second agent: report-agent ran at: 2026-09-05t02:00:00.000000002z"}},
	} {
		matches, err := search(ctx, test.query)
		require.NoError(t, err)
		require.Equal(t, test.want, matches, test.query)
	}

	matches, err := search(ctx, "CRON-AGENT")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{cronSlack, "web-cron-synced"}, slices.Collect(maps.Keys(matches)), "chats without history are not searched")

	_, err = search(t.Context(), "producer")
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}
