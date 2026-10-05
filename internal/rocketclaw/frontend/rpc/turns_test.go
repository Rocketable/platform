package rpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/require"
)

// testCheckpoint is a running turn as the backend records it: an active-turn
// row with live trace plus the turn record journaled under its turn ID.
type testCheckpoint struct {
	TurnID, ConversationKey, Agent, Model, DisplayModel string
	ReasoningEffort                                     *string
	ReplayInput                                         []json.RawMessage
	ReplayAttribution                                   []rocketcode.ReplayAttribution
	OutputTrace                                         []json.RawMessage
	TokenUsage                                          *rocketcode.TokenUsage
	ResponseID                                          string
}

// testTurns seeds active turns through SQL because their writers are bridge-internal.
type testTurns struct{ db *sql.DB }

func openTestTurns(t *testing.T, dsn string) testTurns {
	t.Helper()

	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	return testTurns{db: db}
}

func (s testTurns) UpsertActiveTurn(ctx context.Context, c *testCheckpoint) error {
	trace, err := json.Marshal(c.OutputTrace)
	if err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	record, err := json.Marshal(struct {
		Record rocketcode.SessionEntry `json:"record"`
	}{rocketcode.SessionEntry{Version: 1, Type: "turn", TurnID: c.TurnID, Agent: c.Agent, Model: c.DisplayModel, ReasoningEffort: c.ReasoningEffort, ReplayAttribution: c.ReplayAttribution, TokenUsage: c.TokenUsage, ReplayInput: c.ReplayInput, ResponseID: c.ResponseID}})
	if err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	now := time.Now().UnixNano()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO active_turns (id, conversation_id, inbound_json, output_trace_json, created_at_unix_ns, updated_at_unix_ns, history_anchor_id)
VALUES ($1, $2, '{}', $3, $4, $4, COALESCE((SELECT MAX(id) FROM session_entries WHERE conversation_id = $2), 0))
ON CONFLICT (id) DO UPDATE SET output_trace_json = excluded.output_trace_json, updated_at_unix_ns = excluded.updated_at_unix_ns`, c.TurnID, c.ConversationKey, string(trace), now); err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `INSERT INTO turn_steps (conversation_id, key, value) VALUES ($1, $2, $3) ON CONFLICT (conversation_id, key) DO UPDATE SET value = excluded.value`, c.ConversationKey, c.TurnID, string(record))
	if err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	return nil
}

func (s testTurns) ClearActiveTurn(ctx context.Context, turnID string) error {
	_, err := s.db.ExecContext(ctx, `WITH turn AS (DELETE FROM active_turns WHERE id = $1 RETURNING conversation_id) DELETE FROM turn_steps WHERE key = $1 AND conversation_id IN (SELECT conversation_id FROM turn)`, turnID)
	if err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	return nil
}

// SetActiveTurnTerminal ends a turn as a delivered stop or failure does: the row stays for the transcript.
func (s testTurns) SetActiveTurnTerminal(ctx context.Context, turnID string, terminal protocol.Terminal) error {
	_, err := s.db.ExecContext(ctx, `UPDATE active_turns SET terminal = $2, phase = 'done' WHERE id = $1`, turnID, terminal)
	if err != nil {
		return fmt.Errorf("seed test turn: %w", err)
	}

	return nil
}

func (s testTurns) RecoverableActiveTurns(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM active_turns WHERE phase <> 'done' ORDER BY id`)
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
