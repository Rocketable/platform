package rpc

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const attachmentChunkBytes = 256 << 10

func attachmentMetadata(conversationID string, attachment *protocol.OutboundAttachment) *Attachment {
	return &Attachment{Id: attachment.ID, Name: attachment.Name, MimeType: attachment.MIMEType, Size: max(attachment.Size, int64(len(attachment.Data))), OriginalUnverified: attachment.OriginalUnverified, ConversationId: conversationID}
}

func (s *Server) uploadAttachment(stream grpc.ServerStream) error {
	ctx := stream.Context()
	if _, err := s.principal(ctx); err != nil {
		return err
	}

	first := &Attachment{}
	if err := stream.RecvMsg(first); err != nil {
		return fmt.Errorf("receive upload metadata: %w", err)
	}

	if err := s.visibleConversation(ctx, first.ConversationId); err != nil {
		return err
	}

	name := filepath.Base(first.Name)
	if first.Name == "" || name == "." || name == ".." || name == "/" || len(first.Data) > attachmentChunkBytes {
		return fmt.Errorf("upload attachment: %w", status.Error(codes.InvalidArgument, "file name and chunks of at most 256 KiB are required"))
	}

	attachment := protocol.OutboundAttachment{ID: rand.Text(), Name: name, Data: append([]byte{}, first.Data...)}

	for {
		chunk := &Attachment{}

		err := stream.RecvMsg(chunk)
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return fmt.Errorf("receive upload chunk: %w", err)
		}

		if len(chunk.Data) > attachmentChunkBytes || chunk.ConversationId != "" || chunk.Name != "" || chunk.Id != "" || chunk.MimeType != "" || chunk.Size != 0 || chunk.OriginalUnverified {
			return fmt.Errorf("upload attachment: %w", status.Error(codes.InvalidArgument, "continuation frames must contain only chunks of at most 256 KiB"))
		}

		attachment.Data = append(attachment.Data, chunk.Data...)
	}

	attachment.MIMEType = protocol.NormalizeMIMEType(http.DetectContentType(attachment.Data))
	if err := s.sessions.SaveAttachment(ctx, first.ConversationId, &attachment, true); err != nil {
		return fmt.Errorf("store upload: %w", err)
	}

	if err := stream.SendMsg(attachmentMetadata(first.ConversationId, &attachment)); err != nil {
		return fmt.Errorf("send upload receipt: %w", err)
	}

	return nil
}

func (s *Server) downloadAttachment(stream grpc.ServerStream) error {
	request := &Attachment{}
	if err := stream.RecvMsg(request); err != nil {
		return fmt.Errorf("receive download request: %w", err)
	}
	// Replay scanning keeps authorization tied to surviving history; index references if histories outgrow this.
	history, err := s.history(stream.Context(), &HistoryRequest{Id: request.ConversationId})
	if err != nil {
		return err
	}

	queue, err := s.listQueue(stream.Context(), &ListQueueRequest{Id: request.ConversationId})
	if err != nil {
		return err
	}

	for _, item := range queue.Items {
		history.Messages = append(history.Messages, &TranscriptEvent{Attachments: item.Attachments})
	}

	for _, message := range history.Messages {
		for _, metadata := range message.Attachments {
			if metadata.Id != request.Id {
				continue
			}

			attachment, err := s.sessions.LoadAttachment(stream.Context(), metadata.ConversationId, request.Id, false)
			if err != nil {
				return fmt.Errorf("load download: %w", err)
			}

			for offset := 0; offset < len(attachment.Data) || offset == 0; offset += attachmentChunkBytes {
				frame := attachmentMetadata(request.ConversationId, &attachment)

				frame.Data = attachment.Data[offset:min(offset+attachmentChunkBytes, len(attachment.Data))]
				if err := stream.SendMsg(frame); err != nil {
					return fmt.Errorf("send download chunk: %w", err)
				}
			}

			return nil
		}
	}

	return fmt.Errorf("download attachment: %w", status.Error(codes.NotFound, "attachment is not referenced by this conversation history or queue"))
}

func (s *Server) uploadContent(ctx context.Context, conversationID, text string, ids []string) (protocol.InboundContent, error) {
	content := protocol.InboundContent{Text: text}
	if len(ids) == 0 {
		return content, nil
	}

	if err := s.visibleConversation(ctx, conversationID); err != nil {
		return content, err
	}

	root, err := os.OpenRoot(s.cfg.Workspace)
	if err != nil {
		return content, fmt.Errorf("open upload workspace: %w", err)
	}

	defer func() { _ = root.Close() }()

	for _, id := range ids {
		attachment, err := s.sessions.LoadAttachment(ctx, conversationID, id, true)
		if errors.Is(err, sql.ErrNoRows) {
			return content, fmt.Errorf("prompt attachment: %w", status.Error(codes.NotFound, "upload is not recorded for this conversation"))
		}

		if err != nil {
			return content, fmt.Errorf("load prompt upload: %w", err)
		}

		dir := filepath.Join("artifacts", "uploads", attachment.ID)
		if err := root.MkdirAll(dir, 0o700); err != nil {
			return content, fmt.Errorf("create upload directory: %w", err)
		}

		path := filepath.Join(dir, attachment.Name)
		if err := root.WriteFile(path, attachment.Data, 0o600); err != nil {
			return content, fmt.Errorf("materialize upload: %w", err)
		}

		content.TextAttachments = append(content.TextAttachments, fmt.Sprintf("attachment:%s %q (workspace path %q)", attachment.ID, attachment.Name, path))
		switch attachment.MIMEType {
		case "image/jpeg", "image/png", "image/webp":
			content.Attachments = append(content.Attachments, protocol.InboundAttachment{Name: attachment.Name, MIMEType: attachment.MIMEType, Data: attachment.Data})
		}
	}

	return content, nil
}
