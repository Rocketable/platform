package backend

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
)

// ForkConversation copies a recorded prefix and returns the excluded user prompt.
// OpenCode V2: packages/app/src/session/commands/fork-dialog.tsx uses an exclusive
// message boundary; packages/tui/src/routes/session/dialog-fork.tsx also allows all.
func (s *SessionService) ForkConversation(ctx context.Context, source string, destination protocol.Conversation, before string) (string, error) {
	if err := rejectShadowMutation(ctx, s.db, source); err != nil {
		return "", err
	}

	history, err := s.ObserveEntries(ctx, source)
	if err != nil {
		return "", err
	}

	var entries []rocketcode.SessionEntry

	producers := []string{source}
	prompt, found := "", before == ""

	for index := range history {
		observed := &history[index]
		if observed.Synced && observed.SourceConversationID == "" {
			continue
		}

		producers = append(producers, cmp.Or(observed.SourceConversationID, source))

		entry := observed.Entry
		if before != "" {
			for i, raw := range entry.ReplayInput {
				if fmt.Sprintf("%d:%d", observed.ID, i) != before {
					continue
				}

				items, err := rocketcode.ReplayInputToParams([]json.RawMessage{raw})
				if err != nil {
					return "", fmt.Errorf("decode fork boundary: %w", err)
				}

				role, text, ok, err := ReplayInputMessageRoleText(&items[0], raw)
				if err != nil {
					return "", err
				}

				if !ok || role != "user" {
					return "", errors.New("fork boundary must be a user message")
				}

				prompt, found = text, true
				entry.ReplayInput = entry.ReplayInput[:i]
				entry.ResponseID, entry.OutputTrace, entry.TokenUsage = "", nil, nil

				break
			}
		}

		if len(entry.ReplayInput) > 0 {
			entries = append(entries, entry)
		}

		if before != "" && found {
			break
		}
	}

	if !found {
		return "", errors.New("fork message is no longer in the session")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin fork: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(ctx, tx, destination.ID); err != nil {
		return "", err
	}

	// The new chat's history is copied below through the indexed append.
	if _, err := tx.ExecContext(ctx, `WITH indexed AS (INSERT INTO message_search_indexed (conversation_id) VALUES ($1))
INSERT INTO managed_conversations (conversation_id, agent, created_by, forked_from) VALUES ($1, $2, $3, $4)`, destination.ID, destination.Agent, destination.CreatedBy, source); err != nil {
		return "", fmt.Errorf("create fork: %w", err)
	}

	prompt, err = s.copySessionPrefix(ctx, tx, destination.ID, entries, producers, prompt)
	if err != nil {
		return "", err
	}

	if _, err := s.reconcileShadows(ctx, tx, destination.ID, destination.Agent); err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit fork: %w", err)
	}

	return prompt, nil
}

// copySessionPrefix copies replay and referenced ownership, never live jobs or
// producer routing. Both a fork and a newly attached shadow use this seed path.
func (s *SessionService) copySessionPrefix(ctx context.Context, db stateStoreDB, destination string, entries []rocketcode.SessionEntry, producers []string, prompt string) (string, error) {
	payload, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("encode fork history: %w", err)
	}

	text := string(payload)

	files, err := queryStrings(ctx, db, `SELECT id FROM attachments WHERE conversation_id = ANY($1) AND strpos($2, id) > 0 ORDER BY id`, "fork attachments", producers, text+prompt)
	if err != nil {
		return "", err
	}

	for _, id := range files {
		data, err := s.attachments.Get(ctx, id)
		if err != nil {
			return "", fmt.Errorf("read fork attachment: %w", err)
		}

		copyID := rand.Text()
		if err := s.attachments.Put(ctx, copyID, data); err != nil {
			return "", fmt.Errorf("copy fork attachment: %w", err)
		}

		if _, err := db.ExecContext(ctx, `INSERT INTO attachments (id, conversation_id, name, mime_type, size, original_unverified, upload) SELECT $1, $2, name, mime_type, size, original_unverified, upload FROM attachments WHERE id = $3`, copyID, destination, id); err != nil {
			return "", fmt.Errorf("record fork attachment: %w", err)
		}

		text, prompt = strings.ReplaceAll(text, id, copyID), strings.ReplaceAll(prompt, id, copyID)
	}

	if err := json.Unmarshal([]byte(text), &entries); err != nil {
		return "", fmt.Errorf("decode fork history: %w", err)
	}

	if err := saveSessionSummary(ctx, db, protocol.SessionSummary{ConversationID: destination}); err != nil {
		return "", err
	}

	for i := range entries {
		if _, err := appendSessionEntryDB(ctx, db, destination, destination, &entries[i]); err != nil {
			return "", err
		}
	}

	// Copied system inputs show their Completion Notes from the finished jobs their IDs list, so
	// copy those as consumed notes that wake nothing. Running jobs stay with the source.
	if _, err := db.ExecContext(ctx, `WITH delegations AS (INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp)
SELECT $1 || substr(e.conversation_id, length(p.id) + 1), e.entry_json, e.entry_timestamp
FROM (SELECT DISTINCT unnest($2::text[]) AS id) p
JOIN session_entries e ON e.conversation_id COLLATE "C" >= p.id || '/' AND e.conversation_id COLLATE "C" < p.id || '0'
ORDER BY e.id)
INSERT INTO background_jobs (conversation_id, job_id, child_key, kind, status, agent, label, call_id, subagent_key, origin_json, sync_destination, runner_id, note_state, wake, claimed_turn_id, created_at_unix_ns, finished_at_unix_ns, result)
SELECT $1, job_id, child_key, kind, status, agent, label, call_id, subagent_key, origin_json, '', runner_id, $3, FALSE, '', created_at_unix_ns, finished_at_unix_ns, result
FROM background_jobs WHERE conversation_id = ANY($2) AND child_key = '' AND status <> $4 AND job_id IN (
    SELECT unnest(string_to_array(i->>'input_id', ' ')) FROM session_entries e, jsonb_array_elements(e.entry_json::jsonb->'replay_input') i
    WHERE e.conversation_id = $1 AND (i->>'prompt_header' = '[System]' OR starts_with(i->>'prompt_header', '[System ')))`, destination, producers, noteConsumed, backgroundRunning); err != nil {
		return "", fmt.Errorf("copy fork delegations and completion notes: %w", err)
	}

	return prompt, s.linkRetainedResults(destination, producers)
}

// linkRetainedResults links, rather than copies, the producers' retained execute output into
// conversationID's, so the copied Completion Notes' result IDs still load in the fork. A link keeps
// each file's modification time, which expires it with its source.
func (s *SessionService) linkRetainedResults(conversationID string, producers []string) error {
	root, found, err := s.openSpill()
	if !found {
		return err
	}

	dir := rocketcodeRetainedDir(conversationID)

	for _, producer := range slices.Compact(slices.Sorted(slices.Values(producers))) {
		source := rocketcodeRetainedDir(producer)

		entries, errRead := fs.ReadDir(root.FS(), source)
		if errors.Is(errRead, fs.ErrNotExist) {
			continue
		}

		err = errors.Join(err, errRead, root.MkdirAll(dir, 0o700))
		for _, entry := range entries {
			// A result swept from the source since the listing stays gone.
			if errLink := root.Link(filepath.Join(source, entry.Name()), filepath.Join(dir, entry.Name())); !errors.Is(errLink, fs.ErrNotExist) {
				err = errors.Join(err, errLink)
			}
		}
	}

	if err = errors.Join(err, root.Close()); err != nil {
		return fmt.Errorf("copy fork retained execute results: %w", err)
	}

	return nil
}
