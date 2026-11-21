package backend

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

func (s *SessionService) SaveAttachment(ctx context.Context, conversationID, turnID string, attachment *protocol.OutboundAttachment, upload bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin attachment: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if upload {
		if err := rejectShadowMutation(ctx, tx, conversationID); err != nil {
			return err
		}
	}

	var id string

	err = queryScalar(ctx, tx, "save attachment", `INSERT INTO attachments (id, conversation_id, name, mime_type, size, upload, turn_id) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (id) DO NOTHING RETURNING id`, &id, attachment.ID, conversationID, attachment.Name, attachment.MIMEType, len(attachment.Data), upload, turnID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}

	if err != nil {
		return err
	}

	if err := s.attachments.Put(ctx, id, attachment.Data); err != nil {
		return fmt.Errorf("store attachment original: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit attachment: %w", errors.Join(err, s.discardUnsavedAttachment(context.WithoutCancel(ctx), id)))
	}

	return nil
}

func (s *SessionService) discardUnsavedAttachment(ctx context.Context, id string) error {
	var saved bool
	if err := queryScalar(ctx, s.db, "check unsaved attachment", `SELECT count(*) > 0 FROM attachments WHERE id = $1`, &saved, id); err != nil || saved {
		return err
	}

	if errDelete := s.attachments.Delete(ctx, id); errDelete != nil {
		if _, errQueue := s.db.ExecContext(ctx, `INSERT INTO pending_attachment_deletions (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, id); errQueue != nil {
			return errors.Join(errDelete, errQueue)
		}
	}

	return nil
}

func (s *SessionService) LoadAttachment(ctx context.Context, conversationID, id string, uploadOnly bool) (protocol.OutboundAttachment, error) {
	attachment, err := s.AttachmentMetadata(ctx, conversationID, id, uploadOnly)
	if err != nil {
		return protocol.OutboundAttachment{}, err
	}

	attachment.Data, err = s.attachments.Get(ctx, id)
	if err != nil {
		return protocol.OutboundAttachment{}, fmt.Errorf("load attachment: %w", err)
	}

	return attachment, nil
}

func (s *SessionService) loadAttachmentData(ctx context.Context, attachments []protocol.OutboundAttachment) error {
	for i := range attachments {
		data, err := s.attachments.Get(ctx, attachments[i].ID)
		if err != nil {
			return fmt.Errorf("load attachment %s: %w", attachments[i].ID, err)
		}

		attachments[i].Data = data
	}

	return nil
}

func (s *SessionService) AttachmentMetadata(ctx context.Context, conversationID, id string, uploadOnly bool) (protocol.OutboundAttachment, error) {
	attachment := protocol.OutboundAttachment{ID: id}

	err := s.db.QueryRowContext(ctx, `SELECT name, mime_type, size FROM attachments WHERE id=$1 AND conversation_id=$2 AND (NOT $3 OR upload)`, id, conversationID, uploadOnly).Scan(&attachment.Name, &attachment.MIMEType, &attachment.Size)
	if err != nil {
		return protocol.OutboundAttachment{}, fmt.Errorf("load attachment metadata: %w", err)
	}

	return attachment, nil
}

func (s *SessionService) TurnAttachments(ctx context.Context, conversationID, turnID string) ([]protocol.OutboundAttachment, error) {
	return queryRows(ctx, s.db, `WITH RECURSIVE roots AS (
    SELECT $1::text AS id
), `+descendantsSQL+`, owners AS (
    SELECT id AS conversation_id FROM roots UNION ALL SELECT conversation_id FROM descendants
)
SELECT a.id, COALESCE(NULLIF(a.conversation_id, $1), ''), a.name, a.mime_type, a.size FROM owners o
JOIN attachments a USING (conversation_id) WHERE a.turn_id = $2 ORDER BY a.seq`, "turn attachments", func(row rowScanner) (protocol.OutboundAttachment, error) {
		var attachment protocol.OutboundAttachment
		if err := row.Scan(&attachment.ID, &attachment.ConversationID, &attachment.Name, &attachment.MIMEType, &attachment.Size); err != nil {
			return protocol.OutboundAttachment{}, fmt.Errorf("scan turn attachment: %w", err)
		}

		return attachment, nil
	}, conversationID, turnID)
}

func (s *SessionService) InputAttachments(ctx context.Context, producer, text string) (string, []protocol.OutboundAttachment, error) {
	return inputAttachmentsDB(ctx, s.db, producer, text)
}

func inputAttachmentsDB(ctx context.Context, db stateStoreDB, producer, text string) (string, []protocol.OutboundAttachment, error) {
	var (
		attachments []protocol.OutboundAttachment
		turns       []string
	)

	for word := range strings.FieldsSeq(text) {
		id, ok := strings.CutPrefix(word, "attachment:")
		if !ok {
			continue
		}

		attachment := protocol.OutboundAttachment{ID: id}

		err := db.QueryRowContext(ctx, `SELECT a.conversation_id, a.name, a.mime_type, a.size FROM attachments a
WHERE a.id = $1 AND a.upload AND (a.conversation_id = $2 OR EXISTS (SELECT 1 FROM shadow_turns t
    WHERE t.child_conversation_id = $2 AND t.parent_source_conversation_id = a.conversation_id AND t.child_turn_id <> ''))`, id, producer).Scan(&attachment.ConversationID, &attachment.Name, &attachment.MIMEType, &attachment.Size)
		if err == nil && attachment.ConversationID != producer {
			if turns == nil {
				entries, errRead := observeTranscriptDB(ctx, db, producer, 0, 0, nil)
				if errRead != nil {
					return "", nil, errRead
				}

				turns = []string{}

				for i := range entries {
					entry := &entries[i]

					items, err := harness.ReplayInputToParams(entry.Entry.ReplayInput)
					if err != nil {
						return "", nil, fmt.Errorf("decode consumed input attachments: %w", err)
					}

					for i := range items {
						role, consumed, message, err := ReplayInputMessageRoleText(&items[i], entry.Entry.ReplayInput[i])
						if err != nil {
							return "", nil, err
						}

						if message && role == "user" && consumed == text {
							turns = append(turns, entry.Entry.TurnID)
							break
						}
					}
				}
			}

			err = db.QueryRowContext(ctx, `SELECT a.conversation_id, a.name, a.mime_type, a.size FROM attachments a
WHERE a.id = $1 AND a.upload AND EXISTS (SELECT 1 FROM shadow_turns t
    WHERE t.child_conversation_id = $2 AND t.parent_source_conversation_id = a.conversation_id
        AND t.child_turn_id <> '' AND t.child_turn_id = ANY($3))`, id, producer, turns).Scan(&attachment.ConversationID, &attachment.Name, &attachment.MIMEType, &attachment.Size)
		}

		if errors.Is(err, sql.ErrNoRows) {
			continue
		}

		if err != nil {
			return "", nil, fmt.Errorf("load input attachment metadata: %w", err)
		}

		attachments = append(attachments, attachment)
	}

	for _, file := range slices.Backward(attachments) {
		before, reference := "", text
		if i := strings.LastIndex(text, "\n\n"); i >= 0 {
			before, reference = text[:i], text[i+2:]
		}

		host, easy := strings.CutPrefix(reference, fmt.Sprintf("attachment:%s %q (path \"", file.ID, file.Name))
		if reference == fmt.Sprintf("attachment:%s %q (workspace path %q)", file.ID, file.Name, filepath.Join("artifacts", "uploads", file.ID, file.Name)) ||
			easy && strings.HasSuffix(host, strings.TrimPrefix(fmt.Sprintf("%q)", "/"+file.ID+"/"+file.Name), `"`)) {
			text = before
		}
	}

	return text, attachments, nil
}

func (s *SessionService) RevertAttachmentMetadata(ctx context.Context, conversationID, id string) (protocol.OutboundAttachment, error) {
	var raw json.RawMessage

	err := queryScalar(ctx, s.db, "read reverted attachment owner", `SELECT e.entry_json->'replay_input'->split_part(m.revert_message_id, ':', 2)::integer
    FROM conversations m JOIN entries e ON e.conversation_id = m.conversation_id
        AND e.id = NULLIF(split_part(m.revert_message_id, ':', 1), '')::bigint
    WHERE m.conversation_id = $1 AND m.revert_message_id <> ''`, &raw, conversationID)
	if err != nil {
		return protocol.OutboundAttachment{}, err
	}

	items, err := harness.ReplayInputToParams([]json.RawMessage{raw})
	if err != nil {
		return protocol.OutboundAttachment{}, fmt.Errorf("decode reverted attachment owner: %w", err)
	}

	role, text, found, err := ReplayInputMessageRoleText(&items[0], raw)
	if err != nil {
		return protocol.OutboundAttachment{}, err
	}

	if found && role == "user" {
		for word := range strings.FieldsSeq(text) {
			if word == "attachment:"+id {
				return s.AttachmentMetadata(ctx, conversationID, id, true)
			}
		}
	}

	return protocol.OutboundAttachment{}, sql.ErrNoRows
}

func (s *SessionService) ReplayAttachments(ctx context.Context, conversationID string, raw json.RawMessage, calls map[string]string) ([]protocol.OutboundAttachment, error) {
	var item struct {
		Type, Name, Arguments, Output string
		CallID                        string `json:"call_id"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, fmt.Errorf("decode attachment replay: %w", err)
	}

	if item.Type == "function_call" && item.Name == attachFilesToolName {
		calls[item.CallID] = item.Arguments
	}

	_, called := calls[item.CallID]

	ids, queued := strings.CutPrefix(item.Output, "queued attachments for final response: ")
	if !called || item.Type != "function_call_output" || !queued {
		return nil, nil
	}

	var attachments []protocol.OutboundAttachment

	for id := range strings.FieldsSeq(ids) {
		attachment, err := s.AttachmentMetadata(ctx, conversationID, id, false)
		if err != nil {
			return nil, err
		}

		attachments = append(attachments, attachment)
	}

	return attachments, nil
}
