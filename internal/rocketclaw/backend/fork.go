package backend

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
)

// ForkConversation copies a recorded prefix and returns the excluded user prompt.
// OpenCode V2: packages/app/src/session/commands/fork-dialog.tsx uses an exclusive
// message boundary; packages/tui/src/routes/session/dialog-fork.tsx also allows all.
func (s *SessionService) ForkConversation(ctx context.Context, source string, destination protocol.Conversation, before string) (string, error) {
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
	// Copy attachment ownership as well as history, so deleting the source cannot
	// break the fork. Only files referenced by the prefix or restored prompt copy.
	payload, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("encode fork history: %w", err)
	}

	text := string(payload)

	files, err := queryStrings(ctx, s.db, `SELECT id FROM attachments WHERE conversation_id = ANY($1) AND strpos($2, id) > 0 ORDER BY id`, "fork attachments", producers, text+prompt)
	if err != nil {
		return "", err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin fork: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `INSERT INTO managed_conversations (conversation_id, agent, created_by, forked_from) VALUES ($1, $2, $3, $4)`, destination.ID, destination.Agent, destination.CreatedBy, source); err != nil {
		return "", fmt.Errorf("create fork: %w", err)
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

		if _, err := tx.ExecContext(ctx, `INSERT INTO attachments (id, conversation_id, name, mime_type, size, original_unverified, upload) SELECT $1, $2, name, mime_type, size, original_unverified, upload FROM attachments WHERE id = $3`, copyID, destination.ID, id); err != nil {
			return "", fmt.Errorf("record fork attachment: %w", err)
		}

		text, prompt = strings.ReplaceAll(text, id, copyID), strings.ReplaceAll(prompt, id, copyID)
	}

	if err := json.Unmarshal([]byte(text), &entries); err != nil {
		return "", fmt.Errorf("decode fork history: %w", err)
	}

	if err := saveSessionSummary(ctx, tx, protocol.SessionSummary{ConversationID: destination.ID}); err != nil {
		return "", err
	}

	for i := range entries {
		if _, err := appendSessionEntryDB(ctx, tx, destination.ID, &entries[i]); err != nil {
			return "", err
		}
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit fork: %w", err)
	}

	return prompt, nil
}
