package backend

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

// SaveAttachment stores immutable original bytes before publishing a reference.
func (s *SessionService) SaveAttachment(ctx context.Context, conversationID string, attachment *protocol.OutboundAttachment, upload bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin attachment: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var id string

	err = tx.QueryRowContext(ctx, `INSERT INTO attachments (id, conversation_id, name, mime_type, size, original_unverified, upload) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (id) DO NOTHING RETURNING id`, attachment.ID, conversationID, attachment.Name, attachment.MIMEType, len(attachment.Data), attachment.OriginalUnverified, upload).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("save attachment: %w", err)
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
			if err := s.SaveAttachment(ctx, conversationID, &attachment, false); err != nil {
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
