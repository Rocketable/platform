package backend

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

// SaveAttachment stores immutable original bytes before publishing a reference.
// turnID names the turn whose final reply sends the file, or is empty.
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

	err = queryScalar(ctx, tx, "save attachment", `INSERT INTO attachments (id, conversation_id, name, mime_type, size, original_unverified, upload, turn_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (id) DO NOTHING RETURNING id`, &id, attachment.ID, conversationID, attachment.Name, attachment.MIMEType, len(attachment.Data), attachment.OriginalUnverified, upload, turnID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}

	if err != nil {
		return err
	}

	if err := s.attachments.Put(ctx, id, attachment.Data); err != nil {
		return fmt.Errorf("store attachment original: %w", err)
	}
	// A failed commit may leave an orphan. Never adopt it with different metadata.
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit attachment: %w", err)
	}

	return nil
}

// LoadAttachment requires the producer scope; callers must authorize its reference.
// uploadOnly restricts prompt consumption to files acquired through upload.
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

// loadAttachmentData refills the bytes that persisted turn state leaves out.
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

// AttachmentMetadata loads stored metadata without transferring the file bytes.
// Callers must authorize the reference within its producer scope.
func (s *SessionService) AttachmentMetadata(ctx context.Context, conversationID, id string, uploadOnly bool) (protocol.OutboundAttachment, error) {
	attachment := protocol.OutboundAttachment{ID: id}

	err := s.db.QueryRowContext(ctx, `SELECT name, mime_type, size, original_unverified FROM attachments WHERE id=$1 AND conversation_id=$2 AND (NOT $3 OR upload)`, id, conversationID, uploadOnly).Scan(&attachment.Name, &attachment.MIMEType, &attachment.Size, &attachment.OriginalUnverified)
	if err != nil {
		return protocol.OutboundAttachment{}, fmt.Errorf("load attachment metadata: %w", err)
	}

	return attachment, nil
}

// TurnAttachments returns, in the order they were attached, the files turnID's final reply sends,
// naming the owner of those its subagent sessions attached.
func (s *SessionService) TurnAttachments(ctx context.Context, conversationID, turnID string) ([]protocol.OutboundAttachment, error) {
	return queryRows(ctx, s.db, `WITH RECURSIVE roots AS (
    SELECT $1::text AS id
), `+descendantsSQL+`, owners AS (
    SELECT id AS conversation_id FROM roots UNION ALL SELECT conversation_id FROM descendants
)
SELECT a.id, COALESCE(NULLIF(a.conversation_id, $1), ''), a.name, a.mime_type, a.size, a.original_unverified FROM owners o
JOIN attachments a USING (conversation_id) WHERE a.turn_id = $2 ORDER BY a.seq`, "turn attachments", func(row rowScanner) (protocol.OutboundAttachment, error) {
		var attachment protocol.OutboundAttachment
		if err := row.Scan(&attachment.ID, &attachment.ConversationID, &attachment.Name, &attachment.MIMEType, &attachment.Size, &attachment.OriginalUnverified); err != nil {
			return protocol.OutboundAttachment{}, fmt.Errorf("scan turn attachment: %w", err)
		}

		return attachment, nil
	}, conversationID, turnID)
}

// InputAttachments strips trailing upload references and returns their metadata
// in text order, including actual owners of surviving correlated shadow inputs.
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

		err := db.QueryRowContext(ctx, `SELECT a.conversation_id, a.name, a.mime_type, a.size, a.original_unverified FROM attachments a
WHERE a.id = $1 AND a.upload AND (a.conversation_id = $2 OR EXISTS (SELECT 1 FROM shadow_turns t
    WHERE t.child_conversation_id = $2 AND t.parent_source_conversation_id = a.conversation_id AND t.child_turn_id <> ''))`, id, producer).Scan(&attachment.ConversationID, &attachment.Name, &attachment.MIMEType, &attachment.Size, &attachment.OriginalUnverified)
		if err == nil && attachment.ConversationID != producer {
			if turns == nil {
				// Replay scanning is bounded by retained history; index consumed references if this becomes costly.
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
			// Correlation and the surviving consumed reference are both required; manifests may have released their payload.
			err = db.QueryRowContext(ctx, `SELECT a.conversation_id, a.name, a.mime_type, a.size, a.original_unverified FROM attachments a
WHERE a.id = $1 AND a.upload AND EXISTS (SELECT 1 FROM shadow_turns t
    WHERE t.child_conversation_id = $2 AND t.parent_source_conversation_id = a.conversation_id
        AND t.child_turn_id <> '' AND t.child_turn_id = ANY($3))`, id, producer, turns).Scan(&attachment.ConversationID, &attachment.Name, &attachment.MIMEType, &attachment.Size, &attachment.OriginalUnverified)
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
		reference := fmt.Sprintf("attachment:%s %q (workspace path %q)", file.ID, file.Name, filepath.Join("artifacts", "uploads", file.ID, file.Name))
		if text == reference {
			text = ""
		} else {
			text = strings.TrimSuffix(text, "\n\n"+reference)
		}
	}

	return text, attachments, nil
}

// RevertAttachmentMetadata authorizes only an upload referenced by the current
// staged user boundary. It never exposes the rest of the hidden replay.
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

// ReplayAttachments resolves metadata for successful attach-tool output, never arbitrary paths.
// calls belongs to one producer and survives entry boundaries during history projection.
func (s *SessionService) ReplayAttachments(ctx context.Context, conversationID string, root *os.Root, raw json.RawMessage, calls map[string]string) ([]protocol.OutboundAttachment, error) {
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

	arguments, called := calls[item.CallID]

	const success = "queued attachments for final response"
	if !called || item.Type != "function_call_output" || (item.Output != success && !strings.HasPrefix(item.Output, success+": ")) {
		return nil, nil
	}

	var attachments []protocol.OutboundAttachment

	if item.Output != success {
		for id := range strings.FieldsSeq(strings.TrimPrefix(item.Output, success+": ")) {
			attachment, err := s.AttachmentMetadata(ctx, conversationID, id, false)
			if err != nil {
				return nil, err
			}

			attachments = append(attachments, attachment)
		}

		return attachments, nil
	}

	var input attachFilesInput
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return nil, fmt.Errorf("decode historical attachments: %w", err)
	}

	for i := range input.Attachments {
		id := fmt.Sprintf("recovered-%x", sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", conversationID, item.CallID, i))))

		attachment, err := s.AttachmentMetadata(ctx, conversationID, id, false)
		if errors.Is(err, sql.ErrNoRows) {
			attachment, err = outboundAttachment(root, &input.Attachments[i])
			if err != nil {
				// Historical files may no longer exist or be reachable within the root.
				continue
			}

			attachment.ID, attachment.OriginalUnverified = id, true
			if err := s.SaveAttachment(ctx, conversationID, "", &attachment, false); err != nil {
				return nil, err
			}

			attachment, err = s.AttachmentMetadata(ctx, conversationID, id, false)
		}

		if err != nil {
			return nil, err
		}

		attachments = append(attachments, attachment)
	}

	return attachments, nil
}
