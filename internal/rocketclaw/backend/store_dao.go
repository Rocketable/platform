package backend

import (
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	harness "github.com/Rocketable/platform/internal/rocketcode"
	"github.com/jackc/pgx/v5/pgconn"
)

type stateDAO struct {
	db stateStoreDB
}

// createConversation preserves selection and runs under the caller's history lock.
func (d stateDAO) createConversation(ctx context.Context, conversation protocol.Conversation) (bool, error) {
	summary, err := loadSessionSummary(ctx, d.db, conversation.ID)
	if err != nil {
		return false, err
	}

	if err := markNewChatIndexed(ctx, d.db, conversation.ID); err != nil {
		return false, err
	}

	// A new conversation's named birth space is created when missing; each aggregate yields one row, NULL without a name.
	created, err := execRows(ctx, d.db, "create conversation", "count created conversation", `WITH existing AS (SELECT count(*) AS n FROM managed_conversations WHERE conversation_id = $1),
space AS (
    INSERT INTO web_spaces (name) SELECT $4::text FROM existing WHERE n = 0 AND $4 <> ''
    ON CONFLICT ((lower(name))) DO UPDATE SET name = web_spaces.name RETURNING id
)
INSERT INTO managed_conversations (conversation_id, agent, created_by, space_id) SELECT $1, $2, $3, max(id) FROM space
ON CONFLICT (conversation_id) DO NOTHING`, conversation.ID, conversation.Agent, conversation.CreatedBy, conversation.Space)
	if err != nil {
		return false, err
	}

	return created > 0, saveSessionSummary(ctx, d.db, summary)
}

func (d stateDAO) observedEntries(ctx context.Context, conversationID string) ([]ObservedSessionEntry, error) {
	return queryRows(ctx, d.db, `WITH `+sessionHistorySQL+`
SELECT id, entry_json, source_conversation_id, synced, revert_index FROM effective_entries
ORDER BY id`, "rocketcode session entries", scanObservedEntry, conversationID)
}

// scanObservedEntry reads id, entry_json, source_conversation_id, synced, and
// revert_index from effective_entries.
// The JSON is decoded before advancing rows; retained fields own their bytes.
// RawBytes also prevents cancellation from closing rows during decoding.
func scanObservedEntry(row rowScanner) (ObservedSessionEntry, error) {
	var (
		entry ObservedSessionEntry
		raw   sql.RawBytes
		index int
	)
	if err := row.Scan(&entry.ID, &raw, &entry.SourceConversationID, &entry.Synced, &index); err != nil {
		return ObservedSessionEntry{}, fmt.Errorf("scan rocketcode session entry: %w", err)
	}

	if err := json.Unmarshal(raw, &entry.Entry); err != nil {
		return ObservedSessionEntry{}, fmt.Errorf("parse rocketcode session entry: %w", err)
	}

	clipRevertEntry(&entry.Entry, index)

	return entry, nil
}

// producer reads routing and effect progress under the caller's source history lock.
// A nil inbound means this conversation has no retained producer routing.
func (d stateDAO) producer(ctx context.Context, conversationID string) (*protocol.InboundMessage, int64, error) {
	var (
		data    []byte
		through int64
	)
	if err := d.db.QueryRowContext(ctx, `SELECT producer_inbound_json, producer_effects_through_id FROM managed_conversations WHERE conversation_id = $1`, conversationID).Scan(&data, &through); err != nil {
		return nil, 0, fmt.Errorf("load producer routing: %w", err)
	}

	var inbound *protocol.InboundMessage

	if data != nil {
		if err := json.Unmarshal(data, &inbound); err != nil {
			return nil, 0, fmt.Errorf("decode producer routing: %w", err)
		}
	}

	return inbound, through, nil
}

// bindProducer is called only after authorized destination resolution. The atomic
// update returns the established owner even when another binder already won.
func (d stateDAO) bindProducer(ctx context.Context, conversationID, destination string) (string, error) {
	var owner string
	if err := queryScalar(ctx, d.db, "bind producer destination", `UPDATE managed_conversations SET producer_inbound_json =
jsonb_set(producer_inbound_json::jsonb, '{SyncDestination}', to_jsonb(COALESCE(NULLIF(producer_inbound_json->>'SyncDestination', ''), $2)))::json
WHERE conversation_id = $1 RETURNING producer_inbound_json->>'SyncDestination'`, &owner, conversationID, destination); err != nil {
		return "", err
	}

	return owner, nil
}

// producerEffects loads only original effects in history order, after progress
// has been read under the same transaction's source history lock.
func (d stateDAO) producerEffects(ctx context.Context, conversationID string, through int64) ([]ObservedSessionEntry, error) {
	return queryRows(ctx, d.db, `SELECT id, entry_json FROM session_entries
WHERE conversation_id = $1 AND id > $2 AND entry_json->>'type' IN ($3, $4)
    AND NOT entry_json::jsonb ? 'sync_source_entry_id' ORDER BY id`, "pending producer effects", func(row rowScanner) (ObservedSessionEntry, error) {
		var (
			entry ObservedSessionEntry
			data  []byte
		)
		if err := row.Scan(&entry.ID, &data); err != nil {
			return entry, fmt.Errorf("scan producer effect: %w", err)
		}

		if err := json.Unmarshal(data, &entry.Entry); err != nil {
			return entry, fmt.Errorf("decode producer effect: %w", err)
		}

		return entry, nil
	}, conversationID, through, producerScheduleEntryType, producerResetEntryType)
}

// advanceProducerEffects commits progress in the caller's effect transaction.
func (d stateDAO) advanceProducerEffects(ctx context.Context, conversationID string, through int64) error {
	_, err := execState(ctx, d.db, "advance producer effects", `UPDATE managed_conversations SET producer_effects_through_id = $2 WHERE conversation_id = $1`, conversationID, through)
	return err
}

// projectProducerEffects applies original effects only to their retained owner,
// under the caller's source and destination history locks. Progress commits with
// the effects; the returned surviving schedules are armed only after commit.
func (d stateDAO) projectProducerEffects(ctx context.Context, source, destination, agent string) (map[string]protocol.ScheduledMessageState, error) {
	inbound, through, err := d.producer(ctx, source)
	if err != nil {
		return nil, err
	}

	if inbound == nil || inbound.SyncDestination != destination {
		return nil, nil
	}

	var active bool
	if err := queryScalar(ctx, d.db, "check producer projection completion", `SELECT EXISTS (SELECT 1 FROM active_turns WHERE conversation_id = $1 AND phase <> $2)`, &active, source, turnDone); err != nil || active {
		return nil, err
	}

	effects, err := d.producerEffects(ctx, source, through)
	if err != nil {
		return nil, err
	}

	// Legacy executable rows precede resumed effects. Rehome them before applying
	// schedule/reset entries, preserving their IDs, due times, and cadence.
	schedules, err := queryMap(ctx, d.db, `UPDATE scheduled_messages SET conversation_id = $2, agent = $3
WHERE conversation_id = $1
RETURNING scheduled_message_id, conversation_id, agent, message, due_at_unix_ns, recurring, interval_ns`, "rehome producer schedules", scheduledMessageScanner{}, source, destination, agent)
	if err != nil {
		return nil, err
	}

	for i := range effects {
		observed := &effects[i]

		switch observed.Entry.Type {
		case producerScheduleEntryType:
			var scheduled protocol.ScheduledMessageState
			if err := json.Unmarshal(observed.Entry.OutputTrace[0], &scheduled); err != nil {
				return nil, fmt.Errorf("decode synced schedule: %w", err)
			}

			scheduled.ConversationID, scheduled.Agent = destination, agent

			id := rand.Text()
			if err := d.putScheduledMessage(ctx, id, &scheduled); err != nil {
				return nil, err
			}

			schedules[id] = scheduled
		case producerResetEntryType:
			if err := d.resetScheduledMessages(ctx, destination); err != nil {
				return nil, err
			}

			clear(schedules)
		}
	}

	if len(effects) > 0 {
		if err := d.advanceProducerEffects(ctx, source, effects[len(effects)-1].ID); err != nil {
			return nil, err
		}
	}

	return schedules, nil
}

// Thread returns the persisted managed conversation state.
func (s *SessionService) Thread(conversationID string) (ThreadState, bool, error) {
	var thread ThreadState

	err := s.db.QueryRowContext(context.Background(), `SELECT agent, created_by, spawned_by, parent_conversation_id FROM managed_conversations WHERE conversation_id = $1`, strings.TrimSpace(conversationID)).Scan(&thread.Agent, &thread.CreatedBy, &thread.spawnedBy, &thread.parent)
	if err == sql.ErrNoRows {
		return ThreadState{}, false, nil
	}

	if err != nil {
		return ThreadState{}, false, fmt.Errorf("read managed conversation: %w", err)
	}

	return thread, true, nil
}

// VoiceSettings is one person's Web voice state: their current voice conversation
// (empty when none), the agent for their next one, their voice, and the transcript view.
type VoiceSettings struct {
	ConversationID string
	Agent          string
	Voice          string
	Transcript     bool
}

// VoiceSettings returns username's voice settings, or the defaults when none are stored.
func (s *SessionService) VoiceSettings(ctx context.Context, username string) (VoiceSettings, error) {
	var settings VoiceSettings

	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(conversation_id, ''), agent, voice, transcript FROM voice_settings WHERE username = $1`, username).Scan(&settings.ConversationID, &settings.Agent, &settings.Voice, &settings.Transcript)
	if errors.Is(err, sql.ErrNoRows) {
		return VoiceSettings{}, nil
	}

	if err != nil {
		return VoiceSettings{}, fmt.Errorf("read voice settings: %w", err)
	}

	return settings, nil
}

// StartVoiceConversation makes conversationID, started with agent, username's current
// voice conversation unless one is already current; the current one then wins.
func (s *SessionService) StartVoiceConversation(ctx context.Context, username, conversationID, agent string) error {
	_, err := execState(ctx, s.db, "start voice conversation", `INSERT INTO voice_settings (username, conversation_id, agent) VALUES ($1, $2, $3)
ON CONFLICT (username) DO UPDATE SET conversation_id = excluded.conversation_id, agent = excluded.agent WHERE voice_settings.conversation_id IS NULL`, username, conversationID, agent)

	return err
}

// ResetVoiceConversation forgets username's current voice conversation while it is still conversationID.
func (s *SessionService) ResetVoiceConversation(ctx context.Context, username, conversationID string) error {
	_, err := execState(ctx, s.db, "reset voice conversation", `UPDATE voice_settings SET conversation_id = NULL WHERE username = $1 AND conversation_id = $2`, username, conversationID)
	return err
}

// UpdateVoiceSettings stores the given settings for username; a nil one keeps its value.
func (s *SessionService) UpdateVoiceSettings(ctx context.Context, username string, agent, voice *string, transcript *bool) error {
	_, err := execState(ctx, s.db, "update voice settings", `INSERT INTO voice_settings (username, agent, voice, transcript) VALUES ($1, COALESCE($2, ''), COALESCE($3, ''), COALESCE($4, FALSE))
ON CONFLICT (username) DO UPDATE SET agent = COALESCE($2, voice_settings.agent), voice = COALESCE($3, voice_settings.voice), transcript = COALESCE($4, voice_settings.transcript)`, username, agent, voice, transcript)

	return err
}

// Space is a Web space every person shares.
type Space struct{ ID, Name string }

// ErrSpaceRejected reports a space name another space has, ignoring case, or deleting the last space.
var ErrSpaceRejected = errors.New("another space has that name, or it is the last space")

// Spaces lists the Web spaces, oldest first.
func (s *SessionService) Spaces(ctx context.Context) ([]Space, error) {
	return queryRows(ctx, s.db, `SELECT id, name FROM web_spaces ORDER BY created_at, id`, "web spaces", func(row rowScanner) (Space, error) {
		var space Space
		return space, row.Scan(&space.ID, &space.Name)
	})
}

// CreateSpace adds a space named name.
func (s *SessionService) CreateSpace(ctx context.Context, name string) error {
	return spaceNameError(execState(ctx, s.db, "create space", `INSERT INTO web_spaces (name) VALUES ($1)`, name))
}

// RenameSpace renames space id to name.
func (s *SessionService) RenameSpace(ctx context.Context, id, name string) error {
	return spaceNameError(execState(ctx, s.db, "rename space", `UPDATE web_spaces SET name = $2 WHERE id = $1`, id, name))
}

// spaceNameError reports a name another space has, ignoring case, as ErrSpaceRejected.
func spaceNameError(_ sql.Result, err error) error {
	if errPg, ok := errors.AsType[*pgconn.PgError](err); ok && errPg.Code == "23505" /* unique_violation */ {
		return fmt.Errorf("%w: %w", ErrSpaceRejected, err)
	}

	return err
}

// DeleteSpace deletes space id unless it is the last space. Sessions born there lose their birth space.
// Locking every space, in id order, makes concurrent deletes take turns, so two cannot remove the last two.
func (s *SessionService) DeleteSpace(ctx context.Context, id string) error {
	deleted, err := execRows(ctx, s.db, "delete space", "count deleted space", `WITH locked AS (SELECT id FROM web_spaces ORDER BY id FOR UPDATE),
others AS (SELECT id FROM locked WHERE id <> $1 LIMIT 1)
DELETE FROM web_spaces USING others WHERE web_spaces.id = $1`, id)
	if err == nil && deleted == 0 {
		return ErrSpaceRejected
	}

	return err
}

// TabState returns username's saved Web tab state and its revision; nil and 0 when none is saved.
func (s *SessionService) TabState(ctx context.Context, username string) (state []byte, revision int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT state, revision FROM web_tab_states WHERE username = $1`, username).Scan(&state, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, nil
	}

	if err != nil {
		return nil, 0, fmt.Errorf("read tab state: %w", err)
	}

	return state, revision, nil
}

// SaveTabState replaces username's saved Web tab state when it was made from the saved revision,
// and reports whether it did.
func (s *SessionService) SaveTabState(ctx context.Context, username string, state []byte, revision int64) (bool, error) {
	saved, err := execRows(ctx, s.db, "save tab state", "count saved tab state", `INSERT INTO web_tab_states (username, state, revision) VALUES ($1, $2, $3 + 1)
ON CONFLICT (username) DO UPDATE SET state = excluded.state, revision = excluded.revision WHERE web_tab_states.revision = $3`, username, state, revision)

	return saved > 0, err
}

// SetThreadAgentIfExists updates a managed conversation agent without creating a thread.
func (s *SessionService) SetThreadAgentIfExists(conversationID, agent string) (bool, error) {
	rows, err := execRows(context.Background(), s.db, "update managed conversation agent", "count managed conversation agent update", `UPDATE managed_conversations SET agent = $1 WHERE conversation_id = $2`, strings.TrimSpace(agent), strings.TrimSpace(conversationID))
	return rows > 0, err
}

func (d stateDAO) externalMCPSession(ctx context.Context, externalConversationID string) (ExternalMCPSessionState, bool, error) {
	_, session, err := (externalMCPSessionScanner{}).scan(d.db.QueryRowContext(ctx, `SELECT external_conversation_id, agent, private_conversation_id, managed_conversation_id, slack_channel, origin_pairs FROM external_mcp_sessions WHERE external_conversation_id = $1`, strings.TrimSpace(externalConversationID)))
	if errors.Is(err, sql.ErrNoRows) {
		return ExternalMCPSessionState{}, false, nil
	}

	if err != nil {
		return ExternalMCPSessionState{}, false, fmt.Errorf("read external MCP session: %w", err)
	}

	return session, true, nil
}

// ExternalMCPSessionByConversationID returns the public ID and binding for either session ID.
func (s *SessionService) ExternalMCPSessionByConversationID(conversationID string) (externalConversationID string, session ExternalMCPSessionState, ok bool, err error) {
	externalConversationID, session, err = (externalMCPSessionScanner{}).scan(s.db.QueryRowContext(context.Background(), `SELECT external_conversation_id, agent, private_conversation_id, managed_conversation_id, slack_channel, origin_pairs FROM external_mcp_sessions WHERE private_conversation_id = $1 OR managed_conversation_id = $2`, strings.TrimSpace(conversationID), strings.TrimSpace(conversationID)))
	if errors.Is(err, sql.ErrNoRows) {
		return "", ExternalMCPSessionState{}, false, nil
	}

	if err != nil {
		return "", ExternalMCPSessionState{}, false, fmt.Errorf("read external MCP session by conversation ID: %w", err)
	}

	return externalConversationID, session, true, nil
}

type externalMCPSessionScanner struct{}

func (externalMCPSessionScanner) scan(scanner rowScanner) (string, ExternalMCPSessionState, error) {
	var (
		externalConversationID string
		session                ExternalMCPSessionState
		privateConversationID  sql.NullString
		originPairs            []byte
	)
	if err := scanner.Scan(&externalConversationID, &session.Agent, &privateConversationID, &session.ManagedConversationID, &session.SlackChannel, &originPairs); err != nil {
		return "", ExternalMCPSessionState{}, fmt.Errorf("scan external MCP session: %w", err)
	}

	if err := json.Unmarshal(originPairs, &session.OriginPairs); err != nil {
		return "", ExternalMCPSessionState{}, fmt.Errorf("decode external MCP origin pairs: %w", err)
	}

	session.PrivateConversationID = privateConversationID.String

	return externalConversationID, session, nil
}

// originPairsJSON stores caller details without runtime-injected keys.
func originPairsJSON(metadata map[string]string) []byte {
	pairs := maps.Clone(metadata)
	maps.DeleteFunc(pairs, func(key, _ string) bool {
		return key == "external_conversation_id" || strings.HasPrefix(key, "rocketclaw_")
	})
	raw, _ := json.Marshal(pairs) // Encoding a string map cannot fail.

	return raw
}

func (d stateDAO) goal(ctx context.Context, conversationID string) (GoalState, bool, error) {
	var (
		goal                 GoalState
		createdAt, updatedAt int64
	)

	err := d.db.QueryRowContext(ctx, `SELECT objective, check_script, max_turns, turns_used, status, note, created_at_unix_ns, updated_at_unix_ns, principal_name, principal_email FROM conversation_goals WHERE conversation_id = $1`, strings.TrimSpace(conversationID)).Scan(&goal.Objective, &goal.CheckScript, &goal.MaxTurns, &goal.TurnsUsed, &goal.Status, &goal.Note, &createdAt, &updatedAt, &goal.PrincipalName, &goal.PrincipalEmail)
	if errors.Is(err, sql.ErrNoRows) {
		return GoalState{}, false, nil
	}

	if err != nil {
		return GoalState{}, false, fmt.Errorf("read goal: %w", fmt.Errorf("scan goal: %w", err))
	}

	goal.CreatedAt = timeFromUnixNano(createdAt)

	goal.UpdatedAt = timeFromUnixNano(updatedAt)
	if strings.TrimSpace(goal.Status) == "" {
		goal.Status = GoalStatusActive
	}

	return goal, true, nil
}

func (d stateDAO) putScheduledMessage(ctx context.Context, id string, message *protocol.ScheduledMessageState) error {
	_, err := execState(ctx, d.db, "put scheduled message", `INSERT INTO scheduled_messages (scheduled_message_id, conversation_id, agent, message, due_at_unix_ns, recurring, interval_ns) VALUES ($1, $2, $3, $4, $5, $6::boolean::integer, $7) ON CONFLICT(scheduled_message_id) DO UPDATE SET conversation_id = excluded.conversation_id, agent = excluded.agent, message = excluded.message, due_at_unix_ns = excluded.due_at_unix_ns, recurring = excluded.recurring, interval_ns = excluded.interval_ns`, strings.TrimSpace(id), strings.TrimSpace(message.ConversationID), strings.TrimSpace(message.Agent), message.Message, timeUnixNano(message.DueAt), message.Recurring, int64(message.Interval))
	return err
}

func (d stateDAO) scheduledMessages(ctx context.Context, conversationID string) (map[string]protocol.ScheduledMessageState, error) {
	query := `SELECT scheduled_message_id, conversation_id, agent, message, due_at_unix_ns, recurring, interval_ns FROM scheduled_messages ORDER BY scheduled_message_id`
	args := []any{}

	if strings.TrimSpace(conversationID) != "" {
		query = `SELECT scheduled_message_id, conversation_id, agent, message, due_at_unix_ns, recurring, interval_ns FROM scheduled_messages WHERE conversation_id = $1 ORDER BY scheduled_message_id`

		args = append(args, strings.TrimSpace(conversationID))
	}

	messages, err := queryMap(ctx, d.db, query, "scheduled messages", scheduledMessageScanner{}, args...)
	if err != nil || len(messages) == 0 {
		return nil, err
	}

	return messages, nil
}

func (d stateDAO) clearParkAfter(ctx context.Context, scheduledID string) error {
	_, err := execState(ctx, d.db, "clear thread queue park", `UPDATE thread_queue SET park_after = '' WHERE park_after = $1`, strings.TrimSpace(scheduledID))
	return err
}

func (d stateDAO) deleteScheduledMessage(ctx context.Context, id string) (bool, error) {
	id = strings.TrimSpace(id)
	if err := d.clearParkAfter(ctx, id); err != nil {
		return false, err
	}

	deleted, err := execRows(ctx, d.db, "delete scheduled message", "count deleted scheduled messages", `DELETE FROM scheduled_messages WHERE scheduled_message_id = $1`, id)

	return deleted > 0, err
}

func (d stateDAO) resetScheduledMessages(ctx context.Context, conversationID string) error {
	conversationID = strings.TrimSpace(conversationID)
	if _, err := d.db.ExecContext(ctx, `UPDATE thread_queue SET park_after = '' WHERE conversation_id = $1`, conversationID); err != nil {
		return fmt.Errorf("clear thread queue parks: %w", err)
	}

	_, err := execState(ctx, d.db, "reset scheduled messages", `DELETE FROM scheduled_messages WHERE conversation_id = $1`, conversationID)

	return err
}

// PutThreadQueueItem persists one Enqueued Slack Message.
func (s *SessionService) PutThreadQueueItem(id string, item *protocol.ThreadQueueItem) error {
	if err := putThreadQueueItem(context.Background(), s.db, id, item); err != nil {
		return fmt.Errorf("store waiting input: %w", err)
	}

	return nil
}

func putThreadQueueItem(ctx context.Context, db stateStoreDB, id string, item *protocol.ThreadQueueItem) error {
	kind := cmp.Or(item.Kind, protocol.InboundKindEnqueue)

	// Both payloads contain only strings, booleans and byte slices.
	content, _ := json.Marshal(item.Content)
	reply, _ := json.Marshal(item.SlackReply)

	var inbound *string

	if item.Inbound != nil {
		data, _ := json.Marshal(item.Inbound)

		inbound = new(string(removeSessionEntryNUL(data)))
	}

	changed, err := execRows(ctx, db, "put thread queue item", "count stored queue items", `INSERT INTO thread_queue (queue_item_id, conversation_id, message, principal, stash_at_unix_ns, position, park_after, slack_channel, slack_ts, kind, content, source, slack_reply, inbound_json) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) ON CONFLICT(queue_item_id) DO UPDATE SET message = excluded.message, principal = excluded.principal, stash_at_unix_ns = excluded.stash_at_unix_ns, position = excluded.position, park_after = excluded.park_after, slack_channel = excluded.slack_channel, slack_ts = excluded.slack_ts, kind = excluded.kind, content = excluded.content, source = excluded.source, slack_reply = excluded.slack_reply, inbound_json = excluded.inbound_json WHERE thread_queue.conversation_id = excluded.conversation_id`, strings.TrimSpace(id), strings.TrimSpace(item.ConversationID), item.Message, item.Principal, timeUnixNano(item.StashAt), item.Position, strings.TrimSpace(item.ParkAfter), item.SlackChannel, item.SlackTS, kind, content, item.Source, reply, inbound)
	if err != nil {
		return fmt.Errorf("put thread queue item: %w", err)
	}

	if changed == 0 {
		return errors.New("queue item ID belongs to another conversation")
	}

	return nil
}

// ThreadQueueForConversation returns Enqueued Slack Messages in stack order.
func (s *SessionService) ThreadQueueForConversation(conversationID string) ([]protocol.ThreadQueueItem, error) {
	return queryRows(context.Background(), s.db, `SELECT queue_item_id, conversation_id, message, principal, stash_at_unix_ns, position, park_after, slack_channel, slack_ts, kind, content, source, slack_reply, inbound_json FROM thread_queue WHERE conversation_id = $1 ORDER BY position, stash_at_unix_ns`, "thread queue", scanThreadQueueItem, strings.TrimSpace(conversationID))
}

// DeleteThreadQueueItem deletes one Enqueued Slack Message.
func (s *SessionService) DeleteThreadQueueItem(id string) error {
	_, err := execState(context.Background(), s.db, "delete thread queue item", `DELETE FROM thread_queue WHERE queue_item_id = $1`, strings.TrimSpace(id))
	return err
}

func (d stateDAO) claimThreadQueueItem(ctx context.Context, conversationID, id string) (protocol.ThreadQueueItem, bool, error) {
	item, err := scanThreadQueueItem(d.db.QueryRowContext(ctx, `DELETE FROM thread_queue WHERE conversation_id = $1 AND queue_item_id = $2 AND kind <> $3 RETURNING queue_item_id, conversation_id, message, principal, stash_at_unix_ns, position, park_after, slack_channel, slack_ts, kind, content, source, slack_reply, inbound_json`, conversationID, id, protocol.InboundKindHeld))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return item, false, fmt.Errorf("claim thread queue item: %w", err)
	}

	return item, err == nil, nil
}

func scanThreadQueueItem(scanner rowScanner) (protocol.ThreadQueueItem, error) {
	var (
		item           protocol.ThreadQueueItem
		stashAt        int64
		content, reply []byte
		inbound        []byte
	)

	if err := scanner.Scan(&item.ID, &item.ConversationID, &item.Message, &item.Principal, &stashAt, &item.Position, &item.ParkAfter, &item.SlackChannel, &item.SlackTS, &item.Kind, &content, &item.Source, &reply, &inbound); err != nil {
		return protocol.ThreadQueueItem{}, fmt.Errorf("scan thread queue item: %w", err)
	}

	if inbound != nil {
		if err := json.Unmarshal(inbound, &item.Inbound); err != nil {
			return item, fmt.Errorf("decode thread queue inbound: %w", err)
		}
	}

	if err := json.Unmarshal(content, &item.Content); err != nil {
		return item, fmt.Errorf("decode thread queue content: %w", err)
	}

	if err := json.Unmarshal(reply, &item.SlackReply); err != nil {
		return item, fmt.Errorf("decode thread queue reply: %w", err)
	}

	item.StashAt = timeFromUnixNano(stashAt)

	return item, nil
}

// turnPhase is an active-turn row's lifecycle phase; only done rows are terminal.
type turnPhase string

const (
	turnRunning    turnPhase = "running"
	turnDelivering turnPhase = "delivering"
	turnDone       turnPhase = "done"
)

// activeTurn is a conversation's durable queue head: a taken request and, once
// finished, the final outbound delivered from it.
type activeTurn struct {
	id, conversationID string
	phase              turnPhase
	inbound            *protocol.InboundMessage
	outbound           *protocol.OutboundMessage
}

// turnFinish is everything a bridge request writes when it moves to delivering.
type turnFinish struct {
	store       sessionStore
	entries     []harness.SessionEntry
	accountGoal bool
	outbound    *protocol.OutboundMessage
	terminal    protocol.Terminal
	hidden      bool // The turn is a hidden run's (hiddenRun).
	// retry is set by finishTurn: the fewest failed wakes among the notes the hidden run's turn
	// released for a retry, or 0 when it released none.
	retry int
}

// conversationJournal stores one conversation's RocketCode turn steps.
type conversationJournal struct {
	store          *SessionService
	conversationID string
	log            *slog.Logger
}

// LoadTurnStep returns one recorded step of a conversation's turns.
func (s *SessionService) LoadTurnStep(ctx context.Context, conversationID, key string) (value json.RawMessage, found bool, err error) {
	err = queryScalar(ctx, s.db, "load turn step", `SELECT value FROM turn_steps WHERE conversation_id = $1 AND key = $2`, &value, conversationID, key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, err
	}

	return value, true, nil
}

// SaveTurnStep records one step of a conversation's turns; the turn's finish removes it.
func (s *SessionService) SaveTurnStep(ctx context.Context, conversationID, key string, value json.RawMessage) error {
	_, err := execState(ctx, s.db, "save turn step", `INSERT INTO turn_steps (conversation_id, key, value) VALUES ($1, $2, $3) ON CONFLICT (conversation_id, key) DO UPDATE SET value = excluded.value`, conversationID, key, string(removeSessionEntryNUL(value)))
	return err
}

func (j conversationJournal) Load(ctx context.Context, key string) (value json.RawMessage, found bool, err error) {
	return j.store.LoadTurnStep(ctx, j.conversationID, key)
}

func (j conversationJournal) Save(ctx context.Context, key string, value json.RawMessage) error {
	return j.store.SaveTurnStep(ctx, j.conversationID, key, value)
}

// SaveTrace records live progress on the request's row; retry turn IDs share it,
// and late writes cannot alter a stopped or failed turn.
func (j conversationJournal) SaveTrace(ctx context.Context, turnID string, trace []json.RawMessage) (err error) {
	startedAt := time.Now()

	defer func() {
		// These are repeated snapshots, not tool starts/finishes or execution durations.
		observed := harness.PublicProgressFromTrace(trace)
		for i := range observed {
			progress := &observed[i]
			if progress.Kind != harness.PublicProgressText {
				j.log.Info("operation observed", "event", "operation_snapshot", "conversation_id", j.conversationID, "turn_id", turnID, "operation_id", progress.ID, "parent_id", progress.ParentID, "kind", progress.Kind, "state", progress.State, "boundary", "journal_observation", "observation_elapsed_ms", time.Since(startedAt).Milliseconds(), "error_type", fmt.Sprintf("%T", err))
			}
		}
	}()

	rowID, _, _ := strings.Cut(turnID, "/")

	data, err := json.Marshal(trace)
	if err != nil {
		return fmt.Errorf("encode turn trace: %w", err)
	}

	_, errExec := execState(ctx, j.store.db, "save turn trace", `UPDATE active_turns SET output_trace_json = $2, updated_at_unix_ns = $3 WHERE id = $1 AND terminal = '' AND output_trace_json::jsonb IS DISTINCT FROM $2::jsonb`, rowID, string(removeSessionEntryNUL(data)), timeUnixNano(time.Now()))

	return errExec
}

// SaveCodeModeCall records one Code Mode record on its own row under the request's row, which
// History adds to the row's trace (turn_trace); like SaveTrace, it cannot alter a stopped or failed
// turn. Setting output_trace_json to itself fires the row's change notifications as a trace save
// does, while Postgres reuses a trace large enough to be stored apart instead of writing it again.
func (j conversationJournal) SaveCodeModeCall(ctx context.Context, turnID, key string, record json.RawMessage) error {
	rowID, _, _ := strings.Cut(turnID, "/")

	_, err := execState(ctx, j.store.db, "save code mode call", `WITH turn AS (
    SELECT id FROM active_turns WHERE id = $1 AND terminal = ''
), saved AS (
    INSERT INTO code_mode_calls (turn_id, key, record) SELECT id, $2, $3 FROM turn
    ON CONFLICT (turn_id, key) DO UPDATE SET record = excluded.record RETURNING turn_id
)
UPDATE active_turns a SET output_trace_json = a.output_trace_json, updated_at_unix_ns = $4 FROM saved WHERE a.id = saved.turn_id`,
		rowID, key, string(removeSessionEntryNUL(record)), timeUnixNano(time.Now()))

	return err
}

// CodeModeCalls returns the turn's Code Mode records in the order first saved. Turn IDs that share
// the request's row (SaveTrace) keep their records apart: a record's key is a call key of its turn.
func (j conversationJournal) CodeModeCalls(ctx context.Context, turnID string) ([]json.RawMessage, error) {
	rowID, _, _ := strings.Cut(turnID, "/")

	records, err := queryStrings(ctx, j.store.db, `SELECT record FROM code_mode_calls WHERE turn_id = $1 AND starts_with(key, $2 || '/call/') ORDER BY id`, "code mode calls", rowID, turnID)

	saved := make([]json.RawMessage, 0, len(records))
	for _, record := range records {
		saved = append(saved, json.RawMessage(record))
	}

	return saved, err
}

// startTurnDB records a taken request as its conversation's running queue head.
func startTurnDB(ctx context.Context, db stateStoreDB, turnID, conversationID string, inbound *protocol.InboundMessage) error {
	data, _ := json.Marshal(inbound)

	now := timeUnixNano(time.Now())
	if _, err := db.ExecContext(ctx, `INSERT INTO active_turns (id, conversation_id, inbound_json, output_trace_json, created_at_unix_ns, updated_at_unix_ns, history_anchor_id)
VALUES ($1, $2, $3, '[]', $4, $4, COALESCE((SELECT MAX(id) FROM session_entries WHERE conversation_id = $2), 0))`, turnID, conversationID, string(removeSessionEntryNUL(data)), now); err != nil {
		return fmt.Errorf("start active turn: %w", err)
	}

	if inbound.SyncDestination != "" || inbound.RequireOutputDecision {
		if _, err := db.ExecContext(ctx, `UPDATE managed_conversations SET producer_inbound_json =
($2::jsonb || jsonb_build_object('SyncDestination', COALESCE(NULLIF(producer_inbound_json->>'SyncDestination', ''), $2::jsonb->>'SyncDestination')))::json
WHERE conversation_id = $1`, conversationID, string(removeSessionEntryNUL(data))); err != nil {
			return fmt.Errorf("retain producer routing: %w", err)
		}
	}

	return nil
}

// headTurn returns the non-terminal row that runs on workerID: its own, or a
// private producer's whose sync destination it is.
func (s *SessionService) headTurn(ctx context.Context, workerID string) (activeTurn, bool, error) {
	var (
		turn              activeTurn
		inbound, outbound []byte
	)

	err := s.db.QueryRowContext(ctx, `SELECT id, conversation_id, phase, inbound_json, outbound_json FROM active_turns
WHERE phase <> $2 AND COALESCE(NULLIF(inbound_json->>'SyncDestination', ''), conversation_id) = $1 ORDER BY created_at_unix_ns LIMIT 1`, workerID, turnDone).Scan(&turn.id, &turn.conversationID, &turn.phase, &inbound, &outbound)
	if errors.Is(err, sql.ErrNoRows) {
		return activeTurn{}, false, nil
	}

	if err != nil {
		return activeTurn{}, false, fmt.Errorf("load active turn: %w", err)
	}

	if err := json.Unmarshal(inbound, &turn.inbound); err != nil {
		return activeTurn{}, false, fmt.Errorf("decode active turn inbound: %w", err)
	}

	if outbound != nil {
		if err := json.Unmarshal(outbound, &turn.outbound); err != nil {
			return activeTurn{}, false, fmt.Errorf("decode active turn outbound: %w", err)
		}

		if err := s.loadAttachmentData(ctx, turn.outbound.Attachments); err != nil {
			return activeTurn{}, false, err
		}
	}

	return turn, true, nil
}

// HasActiveTurn reports whether the conversation has a running or undelivered turn.
func (s *SessionService) HasActiveTurn(ctx context.Context, conversationID string) (bool, error) {
	var active bool
	if err := queryScalar(ctx, s.db, "check active turn", `SELECT EXISTS (SELECT 1 FROM active_turns WHERE conversation_id = $1 AND phase <> $2)`, &active, conversationID, turnDone); err != nil {
		return false, err
	}

	return active, nil
}

// finishTurn moves a running row to delivering in one transaction with its
// history, goal accounting, Completion Note claims, and journal cleanup that
// keeps Background Jobs' steps, and returns the outbound to
// deliver. When another path, such as $stop, finished the row first, that
// path's stored outbound wins and nothing else is written.
func (s *SessionService) finishTurn(ctx context.Context, turnID string, finish *turnFinish) (result *protocol.OutboundMessage, err error) {
	defer func() {
		if err != nil {
			err = inputPersistenceError{cause: err}
		}
	}()

	outbound, err := json.Marshal(finish.outbound)
	if err != nil {
		return nil, fmt.Errorf("encode final outbound: %w", err)
	}

	tx, err := s.beginStateTx(ctx, "turn finish")
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// commitRevertDB also takes the history lock before the turn row.
	if err := lockSessionHistory(ctx, tx, finish.store.conversationID); err != nil {
		return nil, err
	}

	// Serialize child saving with parent closure before changing execution phase,
	// so neither settlement can miss the other's uncommitted payload ownership.
	if _, err := tx.ExecContext(ctx, `SELECT id FROM shadow_turns WHERE child_conversation_id = $1 AND child_turn_id = $2 FOR UPDATE`, finish.store.conversationID, turnID); err != nil {
		return nil, fmt.Errorf("lock comparison settlement: %w", err)
	}

	// Closure belongs to settlement, including fallback, preparation failure,
	// workflow completion and stop. Unprepared turns remain terminal-only.
	if _, err := tx.ExecContext(ctx, `UPDATE shadow_turns SET input_closed = TRUE,
terminal = CASE WHEN terminal <> '' THEN terminal WHEN (child_turn_id = '' AND jsonb_array_length(inputs) = 0 AND COALESCE(context_entries->>'workflow_source', '') = '') OR $3 = 'stopped' THEN $3 ELSE terminal END
WHERE parent_source_conversation_id = $1 AND parent_turn_id = $2`, finish.store.conversationID, turnID, cmp.Or(finish.outbound.Terminal, protocol.TerminalComplete)); err != nil {
		return nil, inputPersistenceError{cause: fmt.Errorf("close mirrored input: %w", err)}
	}

	moved, err := execRows(ctx, tx, "finish active turn", "count finished active turn", `UPDATE active_turns SET phase = $2, terminal = $4, updated_at_unix_ns = $5,
outbound_json = (SELECT json_object_agg(key, COALESCE(reply.value, payload.value))
    FROM json_each($3::json) payload
    FULL JOIN (SELECT 'ReplyState' AS key, value FROM turn_steps WHERE conversation_id = $7 AND key = $8) reply USING (key))
WHERE id = $1 AND phase = $6`, turnID, turnDelivering, string(removeSessionEntryNUL(outbound)), finish.terminal, timeUnixNano(time.Now()), turnRunning, finish.store.conversationID, protocol.ReplyStepKey(turnID))
	if err != nil {
		return nil, err
	}

	if moved == 0 {
		var stored protocol.OutboundMessage

		if err := tx.QueryRowContext(ctx, `SELECT outbound_json FROM active_turns WHERE id = $1`, turnID).Scan(&outbound); err != nil {
			return nil, fmt.Errorf("load finished turn outbound: %w", err)
		}

		if err := json.Unmarshal(outbound, &stored); err != nil {
			return nil, fmt.Errorf("decode finished turn outbound: %w", err)
		}

		// A runner arriving after its turn ended may have rewritten the record.
		if err := s.indexTurnMessages(ctx, tx, finish.store.conversationID, turnID); err != nil {
			return nil, err
		}

		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit late turn finish: %w", err)
		}

		return &stored, s.loadAttachmentData(ctx, stored.Attachments)
	}

	if err := finish.preserveStoppedInput(ctx, tx, turnID); err != nil {
		return nil, err
	}

	for i := range finish.entries {
		if _, err := appendSessionEntryDB(ctx, tx, finish.store.conversationID, finish.store.conversationID, &finish.entries[i]); err != nil {
			return nil, err
		}
	}

	if finish.terminal != "" {
		if err := s.indexTurnMessages(ctx, tx, finish.store.conversationID, turnID); err != nil {
			return nil, err
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM thread_queue q WHERE q.conversation_id = $1 AND (EXISTS (
    SELECT 1 FROM session_entries e CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry_json::jsonb->'replay_input', 'null'::jsonb)) item
    WHERE e.conversation_id = q.conversation_id AND item->>'input_id' = q.queue_item_id) OR EXISTS (
    SELECT 1 FROM turn_steps s WHERE s.conversation_id = q.conversation_id AND s.key = $2
    AND s.value::jsonb->'applied_input_ids' ? q.queue_item_id))`, finish.store.conversationID, turnID); err != nil {
		return nil, fmt.Errorf("release recorded input ownership: %w", err)
	}

	if finish.accountGoal {
		if _, err := tx.ExecContext(ctx, `UPDATE conversation_goals SET turns_used = turns_used + 1, status = CASE WHEN max_turns > 0 AND turns_used + 1 >= max_turns THEN $1 ELSE $2 END, updated_at_unix_ns = $3 WHERE conversation_id = $4 AND (status = '' OR status = $2)`, GoalStatusBudgetExhausted, GoalStatusActive, timeUnixNano(time.Now()), finish.store.conversationID); err != nil {
			return nil, fmt.Errorf("account goal turn: %w", err)
		}
	}

	// A turn that saved entries delivered its claimed notes; one that saved none, such as a
	// failed wake, releases them to the next turn without waking for them again, as OpenCode's
	// next turn still sees a failed turn's promoted input (packages/core/src/session/input.ts).
	// A hidden run may have no next turn, so its main agent's notes wake it again 1 minute after
	// the first such turn, 5 minutes after the second, and every 30 minutes after that.
	var errSettle error

	switch {
	case len(finish.entries) > 0:
		_, errSettle = tx.ExecContext(ctx, `UPDATE background_jobs SET note_state = $3 WHERE conversation_id = $1 AND claimed_turn_id = $2 AND note_state = 'pending'`, finish.store.conversationID, turnID, noteConsumed)
	case finish.hidden:
		errSettle = tx.QueryRowContext(ctx, `WITH released AS (UPDATE background_jobs SET claimed_turn_id = '', wake = TRUE, wake_attempts = wake_attempts + 1,
wake_after_unix_ns = $4 + ($5::bigint[])[LEAST(wake_attempts + 1, 3)] WHERE conversation_id = $1 AND claimed_turn_id = $2 AND note_state = $3 RETURNING wake_attempts)
SELECT COALESCE(MIN(wake_attempts), 0) FROM released`, finish.store.conversationID, turnID, notePending, timeUnixNano(time.Now()), []int64{int64(time.Minute), int64(5 * time.Minute), int64(30 * time.Minute)}).Scan(&finish.retry)
	default:
		_, errSettle = tx.ExecContext(ctx, `UPDATE background_jobs SET claimed_turn_id = '', wake = FALSE WHERE conversation_id = $1 AND claimed_turn_id = $2 AND note_state = $3`, finish.store.conversationID, turnID, notePending)
	}

	if errSettle != nil {
		return nil, fmt.Errorf("settle background note claims: %w", errSettle)
	}

	if _, err := tx.ExecContext(ctx, deleteUnownedStepsSQL+`t.conversation_id = $1 AND (starts_with(t.key, $2 || '/') OR t.key = $2 AND $3 = '')`, finish.store.conversationID, turnID, finish.terminal); err != nil {
		return nil, fmt.Errorf("clear turn steps: %w", err)
	}

	if err := releaseShadowPayloads(ctx, tx, finish.store.conversationID, turnID); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit turn finish: %w", err)
	}

	s.jobs.turnFinished()

	return finish.outbound, nil
}

// preserveStoppedInput saves initial human input and its shadow copy without
// promoting unfinished provider/tool output or consuming Completion Notes.
func (f *turnFinish) preserveStoppedInput(ctx context.Context, db stateStoreDB, turnID string) error {
	if f.terminal != protocol.TerminalStopped || len(f.entries) > 0 {
		return nil
	}

	entries, err := queryRows(ctx, db, `SELECT s.value::jsonb->'record' || jsonb_build_object('output_trace', turn_trace(a.id, a.output_trace_json))
FROM turn_steps s JOIN active_turns a ON a.conversation_id = s.conversation_id AND a.id = s.key
LEFT JOIN shadow_turns t ON t.child_conversation_id = a.conversation_id AND t.child_turn_id = a.id
WHERE s.conversation_id = $1 AND s.key = $2 AND COALESCE(s.value->'record'->>'response_id', '') = ''
    AND COALESCE(t.inbound_json, a.inbound_json)->>'Human' = 'true'
    AND jsonb_array_length(s.value::jsonb->'applied_input_ids') > 0`, "stopped input", func(row rowScanner) (harness.SessionEntry, error) {
		var (
			raw   []byte
			entry harness.SessionEntry
		)
		if err := row.Scan(&raw); err != nil {
			return entry, fmt.Errorf("read stopped input: %w", err)
		}

		if err := json.Unmarshal(raw, &entry); err != nil {
			return entry, fmt.Errorf("decode stopped input: %w", err)
		}

		return entry, nil
	}, f.store.conversationID, turnID)
	if err != nil {
		return err
	}

	for i := range entries {
		if _, err := appendSessionEntryDB(ctx, db, f.store.conversationID, f.store.conversationID, &entries[i]); err != nil {
			return err
		}
	}

	return nil
}

// deleteUnownedStepsSQL deletes the turn_steps t starting with the key $2 that the caller's appended
// condition on t selects, except the steps of the running and queued Background Jobs nested under the
// key prefix $2, which own them.
const deleteUnownedStepsSQL = `WITH unowned_steps AS (
    SELECT s.conversation_id, s.key FROM turn_steps s LEFT JOIN background_jobs j ON j.conversation_id = s.conversation_id
        AND j.status = 'running' AND starts_with(j.job_id, $2 || '/') AND starts_with(s.key, j.job_id || '/')
    WHERE starts_with(s.key, $2) AND j.job_id IS NULL
)
DELETE FROM turn_steps t USING unowned_steps u WHERE (t.conversation_id, t.key) = (u.conversation_id, u.key) AND `

// closeTurn ends a delivered row; stopped and failed rows stay as transcript history.
// Steps recorded while delivering (a posted cron root) go with it.
// Web owners without replay identity, and goal kickoffs with generated prompt
// framing, stay done so retries and editable commands retain their original input.
func (s *SessionService) closeTurn(ctx context.Context, turnID string) error {
	_, err := execState(ctx, s.db, "close active turn", `WITH replay AS (
    SELECT 1 FROM active_turns a JOIN session_entries e USING (conversation_id)
    CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry_json::jsonb->'replay_input', 'null'::jsonb)) item
    WHERE a.id = $2 AND a.inbound_json->>'Source' = 'web' AND item->>'input_id' = a.inbound_json->'Metadata'->>'web_message_id' LIMIT 1
), replays AS (SELECT count(*) AS n FROM replay),
delivered AS (DELETE FROM active_turns USING replays WHERE id = $2 AND terminal = '' AND
    NOT (inbound_json->>'Source' = 'web' AND COALESCE(inbound_json->'Metadata'->>'web_message_id', '') <> '' AND
        (inbound_json->>'GoalAction' = 'goal' OR n = 0)) RETURNING id),
delivery_steps AS (`+deleteUnownedStepsSQL+`starts_with(t.key, $2 || '/')),
kept AS (SELECT count(*) = 0 AS undelivered FROM delivered)
UPDATE active_turns SET phase = $1 FROM kept WHERE id = $2 AND undelivered`, turnDone, turnID)

	return err
}

// backgroundJobKind is the work a Background Job runs; a task waits for a subagent session's turn.
type backgroundJobKind string

const (
	backgroundExecute backgroundJobKind = "execute"
	backgroundTask    backgroundJobKind = "task"
)

// backgroundJobStatus is a Background Job's lifecycle state; only running is not terminal.
type backgroundJobStatus string

const (
	backgroundRunning   backgroundJobStatus = "running"
	backgroundCompleted backgroundJobStatus = "completed"
	backgroundFailed    backgroundJobStatus = "failed"
	backgroundStopped   backgroundJobStatus = "stopped"
	backgroundKilled    backgroundJobStatus = "killed"
)

// noteState tracks a Background Job's Completion Note.
type noteState string

const (
	noteNone     noteState = "none"
	notePending  noteState = "pending"
	noteConsumed noteState = "consumed"
)

// backgroundJob is one durable Background Job row, keyed by its owning
// conversation and its tool call key. The job owns the journal steps under jobID + "/".
type backgroundJob struct {
	conversationID, jobID string
	kind                  backgroundJobKind
	status                backgroundJobStatus
	agent, label, callID  string
	// subagentKey is a task's subagent session: conversationID + subagentKey.
	subagentKey string
	// origin is the originating turn's frozen routing; its SyncDestination
	// also lists the job at that conversation.
	origin    *protocol.InboundMessage
	runnerID  string
	noteState noteState
	wake      bool
	// wakeAfter is when a note a hidden run released may wake it again (finishTurn).
	wakeAfter     time.Time
	claimedTurnID string
	createdAt     time.Time
	finishedAt    time.Time
	// result is the job's output, or its error when it failed.
	result string
	// input is a task's prompt, which its subagent's answer is reviewed with after a restart.
	input string
}

const backgroundJobColumns = `conversation_id, job_id, kind, status, agent, label, call_id, subagent_key, origin_json, runner_id, note_state, wake, wake_after_unix_ns, claimed_turn_id, created_at_unix_ns, finished_at_unix_ns, result, input`

func scanBackgroundJob(scanner rowScanner) (backgroundJob, error) {
	var (
		job                              backgroundJob
		origin                           []byte
		wakeAfter, createdAt, finishedAt int64
	)

	if err := scanner.Scan(&job.conversationID, &job.jobID, &job.kind, &job.status, &job.agent, &job.label, &job.callID, &job.subagentKey, &origin, &job.runnerID, &job.noteState, &job.wake, &wakeAfter, &job.claimedTurnID, &createdAt, &finishedAt, &job.result, &job.input); err != nil {
		return backgroundJob{}, fmt.Errorf("scan background job: %w", err)
	}

	if err := json.Unmarshal(origin, &job.origin); err != nil {
		return backgroundJob{}, fmt.Errorf("decode background job origin: %w", err)
	}

	job.wakeAfter, job.createdAt, job.finishedAt = timeFromUnixNano(wakeAfter), timeFromUnixNano(createdAt), timeFromUnixNano(finishedAt)

	return job, nil
}

// createBackgroundJob records a running job once per (conversation, job ID)
// and returns the stored row; created is false when the row already existed.
func createBackgroundJob(ctx context.Context, db stateStoreDB, job *backgroundJob) (stored backgroundJob, created bool, err error) {
	origin, _ := json.Marshal(job.origin)

	inserted, err := execRows(ctx, db, "create background job", "count created background job", `INSERT INTO background_jobs (conversation_id, job_id, kind, status, agent, label, call_id, subagent_key, origin_json, sync_destination, runner_id, note_state, wake, claimed_turn_id, created_at_unix_ns, finished_at_unix_ns, result, input)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, FALSE, '', $13, 0, '', $14) ON CONFLICT (conversation_id, job_id) DO NOTHING`, job.conversationID, job.jobID, job.kind, backgroundRunning, job.agent, job.label, job.callID, job.subagentKey, string(removeSessionEntryNUL(origin)), job.origin.SyncDestination, job.runnerID, noteNone, timeUnixNano(time.Now()), strings.ReplaceAll(job.input, "\x00", ""))
	if err != nil {
		return backgroundJob{}, false, err
	}

	stored, err = scanBackgroundJob(db.QueryRowContext(ctx, `SELECT `+backgroundJobColumns+` FROM background_jobs WHERE conversation_id = $1 AND job_id = $2`, job.conversationID, job.jobID))

	return stored, inserted > 0, err
}

// finishBackgroundJob ends a running job held by job.runnerID and, in the same transaction, deletes its
// journal steps except those of the running jobs it started. It reports false when another transition
// or runner won.
func (s *SessionService) finishBackgroundJob(ctx context.Context, job *backgroundJob) (bool, error) {
	tx, err := s.beginStateTx(ctx, "background job finish")
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	finished, err := execRows(ctx, tx, "finish background job", "count finished background job", `UPDATE background_jobs SET status = $4, note_state = $5, wake = $6, result = $7, finished_at_unix_ns = $8
WHERE conversation_id = $1 AND job_id = $2 AND runner_id = $3 AND status = $9`, job.conversationID, job.jobID, job.runnerID, job.status, job.noteState, job.wake, strings.ReplaceAll(job.result, "\x00", ""), timeUnixNano(time.Now()), backgroundRunning)
	if err != nil || finished == 0 {
		return false, err
	}

	if _, err := tx.ExecContext(ctx, deleteUnownedStepsSQL+`t.conversation_id = $1 AND starts_with(t.key, $2 || '/')`, job.conversationID, job.jobID); err != nil {
		return false, fmt.Errorf("clear background job steps: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit background job finish: %w", err)
	}

	return true, nil
}

// restampBackgroundJob hands a running job held by job.runnerID to runnerID.
func (s *SessionService) restampBackgroundJob(ctx context.Context, job *backgroundJob, runnerID string) (bool, error) {
	moved, err := execRows(ctx, s.db, "restamp background job", "count restamped background job", `UPDATE background_jobs SET runner_id = $4 WHERE conversation_id = $1 AND job_id = $2 AND runner_id = $3 AND status = $5`, job.conversationID, job.jobID, job.runnerID, runnerID, backgroundRunning)
	return moved > 0, err
}

// claimBackgroundNotes claims the conversation's unclaimed pending notes for turnID,
// skipping notes another transaction is claiming, in finish order. With held it
// also returns the pending notes turnID already holds, for a resumed turn.
func claimBackgroundNotes(ctx context.Context, db stateStoreDB, conversationID, turnID string, held bool) ([]backgroundJob, error) {
	return queryRows(ctx, db, `WITH unclaimed AS (
    SELECT job_id AS unclaimed_id FROM background_jobs WHERE conversation_id = $1 AND note_state = $3 AND claimed_turn_id = '' FOR UPDATE SKIP LOCKED
), claimed AS (UPDATE background_jobs SET claimed_turn_id = $2 FROM unclaimed WHERE conversation_id = $1 AND job_id = unclaimed_id RETURNING `+backgroundJobColumns+`)
SELECT * FROM claimed UNION ALL SELECT `+backgroundJobColumns+` FROM background_jobs WHERE $4 AND conversation_id = $1 AND note_state = $3 AND claimed_turn_id = $2 ORDER BY finished_at_unix_ns, job_id`, "background note claims", scanBackgroundJob, conversationID, turnID, notePending, held)
}

// redirectBackgroundJobs makes a hidden run's jobs without a destination report to destinationID.
func (d stateDAO) redirectBackgroundJobs(ctx context.Context, conversationID, destinationID string) error {
	_, err := execState(ctx, d.db, "redirect background jobs", `UPDATE background_jobs SET sync_destination = $2, origin_json = (origin_json::jsonb || jsonb_build_object('SyncDestination', $2::text))::json WHERE conversation_id = $1 AND sync_destination = ''`, conversationID, destinationID)
	return err
}

// backgroundWake returns the conversation's unclaimed Completion Note that may wake it first, if any:
// the earliest finished of those that may wake it now, or else the one whose wakeAfter comes first.
func (s *SessionService) backgroundWake(ctx context.Context, conversationID string) (backgroundJob, bool, error) {
	job, err := scanBackgroundJob(s.db.QueryRowContext(ctx, `SELECT `+backgroundJobColumns+` FROM background_jobs WHERE conversation_id = $1 AND note_state = $2 AND wake AND claimed_turn_id = ''
ORDER BY GREATEST(wake_after_unix_ns, $3), finished_at_unix_ns, job_id LIMIT 1`, conversationID, notePending, timeUnixNano(time.Now())))
	if errors.Is(err, sql.ErrNoRows) {
		return backgroundJob{}, false, nil
	}

	return job, err == nil, err
}

// notedBackgroundJobs returns the conversation's jobs listed in jobIDs, in finish order.
func (s *SessionService) notedBackgroundJobs(ctx context.Context, conversationID string, jobIDs []string) ([]backgroundJob, error) {
	return queryRows(ctx, s.db, `SELECT `+backgroundJobColumns+` FROM background_jobs WHERE conversation_id = $1 AND job_id = ANY($2) ORDER BY finished_at_unix_ns, job_id`, "noted background jobs", scanBackgroundJob, conversationID, jobIDs)
}

// releaseStaleBackgroundClaims frees pending notes claimed by turns that are no longer running.
func (s *SessionService) releaseStaleBackgroundClaims(ctx context.Context) error {
	_, err := execState(ctx, s.db, "release stale background note claims", `UPDATE background_jobs j SET claimed_turn_id = '' WHERE note_state = $1 AND claimed_turn_id <> ''
AND NOT EXISTS (SELECT 1 FROM active_turns a WHERE a.id = j.claimed_turn_id AND a.phase = $2)`, notePending, turnRunning)

	return err
}

// backgroundJobs lists a conversation's running jobs and undelivered notes,
// including those of hidden runs whose sync destination it is.
func (s *SessionService) backgroundJobs(ctx context.Context, conversationID string) ([]backgroundJob, error) {
	return queryRows(ctx, s.db, `SELECT `+backgroundJobColumns+` FROM background_jobs WHERE (conversation_id = $1 OR sync_destination = $1) AND (status = $2 OR note_state = $3) ORDER BY created_at_unix_ns, job_id`, "background jobs", scanBackgroundJob, conversationID, backgroundRunning, notePending)
}

// runningBackgroundJobs returns every running job, for startup recovery.
func (s *SessionService) runningBackgroundJobs(ctx context.Context) ([]backgroundJob, error) {
	return queryRows(ctx, s.db, `SELECT `+backgroundJobColumns+` FROM background_jobs WHERE status = $1 ORDER BY created_at_unix_ns, conversation_id, job_id`, "running background jobs", scanBackgroundJob, backgroundRunning)
}

func timeUnixNano(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}

	return value.UTC().UnixNano()
}

func timeFromUnixNano(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}

	return time.Unix(0, value).UTC()
}

type rowScanner interface {
	Scan(...any) error
}

type scheduledMessageScanner struct{}

func (scheduledMessageScanner) scan(scanner rowScanner) (id string, message protocol.ScheduledMessageState, err error) {
	var (
		dueAt, interval int64
		recurring       int
	)
	if err := scanner.Scan(&id, &message.ConversationID, &message.Agent, &message.Message, &dueAt, &recurring, &interval); err != nil {
		return "", protocol.ScheduledMessageState{}, fmt.Errorf("scan scheduled message: %w", err)
	}

	message.DueAt = timeFromUnixNano(dueAt)
	message.Recurring = recurring != 0
	message.Interval = time.Duration(interval)

	return id, message, nil
}

func scanCronSchedule(scanner rowScanner) (CronScheduleState, error) {
	var (
		schedule CronScheduleState
		nextDue  int64
	)

	if err := scanner.Scan(&schedule.ScheduleID, &schedule.RelativePath, &nextDue); err != nil {
		return CronScheduleState{}, fmt.Errorf("scan cron schedule: %w", err)
	}

	schedule.NextDue = timeFromUnixNano(nextDue)

	return schedule, nil
}
