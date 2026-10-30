package rpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"maps"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
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
	cfg := &config.Config{Workspace: t.TempDir()}
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

	for id, run := range map[string]*protocol.CronRun{
		scheduled: {Path: "cron/daily.md", At: time.Date(2026, 9, 5, 1, 0, 0, 1, time.UTC)}, oneOff: {Path: "cron/report.md", At: time.Date(2026, 9, 5, 2, 0, 0, 2, time.UTC), OneOff: true},
	} {
		require.NoError(t, (&backend.Runtime{Sessions: sessions}).CreateConversation(ctx, protocol.Conversation{ID: id, Agent: map[string]string{scheduled: "cron-agent", oneOff: "report-agent"}[id], CreatedBy: "cron", Run: run}))
	}

	for id, thread := range map[string]backend.ThreadState{
		cronSlack: {Agent: "main", CreatedBy: backend.ThreadCreatedByCron}, plainSlack: {Agent: "main", CreatedBy: "U1"},
		"web:" + scheduled: {Agent: "main", CreatedBy: "alice"}, "web:" + oneOff: {Agent: "main", CreatedBy: "alice"},
		"web-cron-synced": {Agent: "main", CreatedBy: "alice"}, "web-later-cron": {Agent: "main", CreatedBy: "alice"}, "web-empty": {Agent: "main", CreatedBy: "alice"},
	} {
		require.NoError(t, sessions.UpsertThread(id, thread))
	}

	// The Slack threads a mention and a cron report create, and the Web chats continuing cron runs.
	_, err = db.ExecContext(ctx, `UPDATE conversations SET kind = 'slack_thread', slack_channel = split_part(conversation_id, ':', 2), slack_thread = split_part(conversation_id, ':', 3)
WHERE conversation_id IN ($1, $2)`, cronSlack, plainSlack)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE conversations SET cron_run = substr(conversation_id, 5) WHERE conversation_id IN ($1, $2)`, "web:"+scheduled, "web:"+oneOff)
	require.NoError(t, err)
	require.NoError(t, sessions.RegisterExternalMCPConversation("ext-1", "main", &backend.ExternalMCPSessionState{Agent: "producer", PrivateConversationID: "external_mcp:private", ManagedConversationID: destination, OriginPairs: map[string]string{"Ticket": "T-1", "b": "two"}}))

	// entry appends a turn; a source attributes it by entry ID, as copies made before 049 do, or by name.
	entry := func(id, source string, byEntryID bool) {
		entryID, err := sessions.AppendEntryID(ctx, id, &harness.SessionEntry{Type: "turn"})
		require.NoError(t, err)

		provenance := `jsonb_build_object('sync_source_conversation_id', $2::text)`
		if byEntryID {
			provenance = `jsonb_build_object('sync_source_entry_id', f.id)`
		}

		if source != "" {
			_, err = db.ExecContext(ctx, `WITH first_entry AS (SELECT MIN(id) AS id FROM entries WHERE conversation_id = $2)
UPDATE entries e SET entry_json = (e.entry_json::jsonb || `+provenance+`)::json FROM first_entry f WHERE e.id = $1`, entryID, source)
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
	_, err = db.ExecContext(ctx, `INSERT INTO turns (id, conversation_id, inbound_json, output_trace_json, created_at_unix_ns, updated_at_unix_ns, history_anchor_id)
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

	require.NoError(t, server.listSessions(&ListSessionsRequest{}, &mockServerStream{ContextFunc: func() context.Context {
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
	// Isolate physical origin from projection even for a copied origin; how far undo
	// reaches is asserted separately by the Revert floor tests.
	for _, id := range []string{"web-cron-synced", "web-later-cron"} {
		_, err = db.ExecContext(ctx, `WITH first_entry AS (SELECT MIN(id) AS id FROM entries WHERE conversation_id = $1)
UPDATE conversations c SET revert_message_id = f.id::text || ':0' FROM first_entry f WHERE c.conversation_id = $1`, id)
		require.NoError(t, err)
		history, err := server.history(ctx, &HistoryRequest{Id: id})
		require.NoError(t, err)
		require.Equal(t, bulk[id], history.Origin, "first-message cutoff cannot erase physical origin")
		require.True(t, history.RevertEligible, "a Web chat can undo whatever its history holds")

		_, err = db.ExecContext(ctx, `UPDATE conversations SET revert_message_id = '' WHERE conversation_id = $1`, id)
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
