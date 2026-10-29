package rpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/require"
)

// testCheckpoint is a running turn as the backend records it: an active-turn
// row with live trace plus the turn record journaled under its turn ID.
type testCheckpoint struct {
	TurnID, ConversationKey, Agent, Model, DisplayModel string
	ReasoningEffort                                     *string
	ReplayInput                                         []json.RawMessage
	ReplayAttribution                                   []harness.ReplayAttribution
	OutputTrace                                         []json.RawMessage
	TokenUsage                                          *harness.TokenUsage
	ResponseID                                          string
}

// testTurns seeds active turns through SQL because their writers are bridge-internal.
type testTurns struct{ db *sql.DB }

func openTestTurns(tb testing.TB, dsn string) testTurns {
	tb.Helper()

	db, err := sql.Open("pgx", dsn)
	require.NoError(tb, err)
	tb.Cleanup(func() { require.NoError(tb, db.Close()) })

	return testTurns{db: db}
}

// RecordSlackThread records id as the Slack thread a mention creates, with the channel and thread its ID names.
func (s testTurns) RecordSlackThread(t *testing.T, sessions *backend.SessionService, id string, thread backend.ThreadState) {
	t.Helper()

	require.NoError(t, sessions.UpsertThread(id, thread))
	channel, ts, _ := strings.Cut(strings.TrimPrefix(id, "slack-thread:"), ":")
	_, err := s.db.ExecContext(t.Context(), `UPDATE conversations SET kind = 'slack_thread', slack_channel = $2, slack_thread = $3 WHERE conversation_id = $1`, id, channel, ts)
	require.NoError(t, err)
}

// RecordReview records the permission review of call callID of turn turnID in parent as
// Bridge.AppendChildEntry does, and returns its ID.
func (s testTurns) RecordReview(tb testing.TB, parent, turnID, callID string) string {
	tb.Helper()

	id := parent + "/" + harness.ReviewKey(turnID, callID)
	_, err := s.db.ExecContext(tb.Context(), `INSERT INTO conversations (conversation_id, agent, created_by, parent_conversation_id, kind, listed, retention, call_turn, call_id)
VALUES ($1, '', '', $2, 'review', FALSE, 'nested', $3, $4)`, id, parent, turnID, callID)
	require.NoError(tb, err)

	return id
}

// RecordLegacyDelegation records the history saved under call callID of parent, as migration 051
// records one saved before delegations had their own key.
func (s testTurns) RecordLegacyDelegation(tb testing.TB, parent, callID string) {
	tb.Helper()

	_, err := s.db.ExecContext(tb.Context(), `INSERT INTO conversations (conversation_id, agent, created_by, parent_conversation_id, kind, listed, retention, call_id)
VALUES ($1 || '/' || $2, '', '', $1, 'subagent', FALSE, 'nested', $2)`, parent, callID)
	require.NoError(tb, err)
}

func (s testTurns) UpsertActiveTurn(ctx context.Context, c *testCheckpoint) error {
	trace, err := json.Marshal(c.OutputTrace)
	if err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	record, err := json.Marshal(struct {
		Record harness.SessionEntry `json:"record"`
	}{harness.SessionEntry{Version: 1, Type: "turn", TurnID: c.TurnID, Agent: c.Agent, Model: c.DisplayModel, ReasoningEffort: c.ReasoningEffort, ReplayAttribution: c.ReplayAttribution, TokenUsage: c.TokenUsage, ResponseID: c.ResponseID}})
	if err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	replay, err := json.Marshal(append([]json.RawMessage{}, c.ReplayInput...))
	if err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	now := time.Now().UnixNano()
	if _, err := s.db.ExecContext(ctx, `WITH last_entry AS (SELECT COALESCE(MAX(id), 0) AS id FROM entries WHERE conversation_id = $2)
INSERT INTO turns (id, conversation_id, inbound_json, output_trace_json, created_at_unix_ns, updated_at_unix_ns, history_anchor_id)
SELECT $1, $2, '{}', $3, $4, $4, l.id FROM last_entry l
ON CONFLICT (id) DO UPDATE SET output_trace_json = excluded.output_trace_json, updated_at_unix_ns = excluded.updated_at_unix_ns`, c.TurnID, c.ConversationKey, string(trace), now); err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	// The turn's step and its replay items, as the backend journals a running turn.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM steps WHERE conversation_id = $1 AND starts_with(key, $2 || '/replay_input/')`, c.ConversationKey, c.TurnID); err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `INSERT INTO steps (conversation_id, key, value)
SELECT $1, $2 || '/replay_input/' || lpad((i.n - 1)::text, 10, '0'), i.item FROM json_array_elements($4::json) WITH ORDINALITY AS i(item, n)
UNION ALL SELECT $1, $2, $3::json ON CONFLICT (conversation_id, key) DO UPDATE SET value = excluded.value`, c.ConversationKey, c.TurnID, string(record), string(replay))
	if err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	return s.recordRunning(ctx)
}

func (s testTurns) ClearActiveTurn(ctx context.Context, turnID string) error {
	_, err := s.db.ExecContext(ctx, `WITH turn AS (DELETE FROM turns WHERE id = $1 RETURNING conversation_id)
DELETE FROM steps s USING turn t WHERE s.conversation_id = t.conversation_id AND (s.key = $1 OR starts_with(s.key, $1 || '/'))`, turnID)
	if err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	return s.recordRunning(ctx)
}

// SetActiveTurnTerminal ends a turn as a delivered stop or failure does: the row stays for the transcript.
func (s testTurns) SetActiveTurnTerminal(ctx context.Context, turnID string, terminal protocol.Terminal) error {
	_, err := s.db.ExecContext(ctx, `UPDATE turns SET terminal = $2, phase = 'done' WHERE id = $1`, turnID, terminal)
	if err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	return s.recordRunning(ctx)
}

func (s testTurns) RecoverableActiveTurns(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM turns WHERE phase NOT IN ('waiting', 'done') ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read test turns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []string

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read test turns: %w", err)
		}

		ids = append(ids, id)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read test turns: %w", err)
	}

	return ids, nil
}

// recordRunning stores on each conversation whether a turn of it has not ended, as the backend's
// turn writes do.
func (s testTurns) recordRunning(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `WITH running AS (
    SELECT DISTINCT conversation_id FROM turns WHERE phase NOT IN ('waiting', 'done')
), facts AS (
    SELECT c.conversation_id, r.conversation_id IS NOT NULL AS running FROM conversations c LEFT JOIN running r USING (conversation_id) WHERE c.kind <> 'remote'
)
UPDATE conversations c SET running = f.running FROM facts f WHERE c.conversation_id = f.conversation_id`)
	if err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	return nil
}
