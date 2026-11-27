package rpc

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

var slackPersonID = regexp.MustCompile(`^[UW][A-Z0-9]+$`)

func distinctMentionIDs(ids []string) ([]string, error) {
	var selected []string

	for _, id := range ids {
		if !slackPersonID.MatchString(id) {
			return nil, fmt.Errorf("validate teammate tags: %w", status.Error(codes.InvalidArgument, "Only people can be tagged."))
		}

		if !slices.Contains(selected, id) {
			if len(selected) == 5 {
				return nil, fmt.Errorf("validate teammate tags: %w", status.Error(codes.InvalidArgument, "Tag at most five people per message."))
			}

			selected = append(selected, id)
		}
	}

	return selected, nil
}

type sessionMentionNotice struct {
	server   *Server
	id       string
	ids      []string
	accepted bool
	warning  *string
}

func (n *sessionMentionNotice) PromptAccepted(ctx context.Context) {
	if len(n.ids) == 0 {
		return
	}

	n.accepted = true

	if _, privateTo, err := n.server.sessions.TopLevelSession(ctx, n.id); err != nil || privateTo != "" {
		if err != nil {
			slog.Warn("session mention privacy lookup failed", "session", n.id, "error", err)
		}

		return
	}

	cfg := n.server.cfg.Clone()

	path := "/s/"
	if strings.HasPrefix(n.id, "opencode:") {
		path = "/oc/"
	}

	path += base64.RawURLEncoding.EncodeToString([]byte(n.id))
	if cfg.Web.Tsnet == nil || cfg.Web.Tsnet.Origin == "" {
		n.warning = new("Teammate notifications could not be sent because the server has no Web link.")
		return
	}

	for _, id := range n.ids {
		if err := n.server.channels.SendSessionMention(ctx, id, cfg.Web.Tsnet.Origin+path); err != nil {
			slog.Warn("session mention delivery failed", "recipient", id, "session", n.id, "error", err)

			n.warning = new("One or more teammate notifications could not be sent.")
		}
	}

	for _, id := range n.ids {
		email := n.server.channels.SlackMentionEmail(ctx, id)
		if email == "" {
			continue
		}

		login, err := n.server.sessions.FindWebLogin(ctx, email)
		if err != nil {
			slog.Warn("session mention login lookup failed", "error", err)
			continue
		}

		if login == "" {
			continue
		}

		recipient := metadata.NewIncomingContext(ctx, metadata.Pairs("rocketclaw-tailnet-login-bin", login, "rocketclaw-tailnet-name-bin", login))
		if err := n.server.visibleConversation(recipient, n.id); err != nil {
			continue
		}

		if err := n.server.sessions.ChangeAttention(ctx, login, []string{path}, backend.AttentionUnread, nil, 0); err != nil {
			slog.Warn("session mention tab mark failed", "error", err)
		}
	}
}

func (s *Server) notifyRemoteSessionMentions(ctx context.Context, request *PromptRequest) (*PromptResponse, error) {
	if _, _, err := s.principal(ctx); err != nil {
		return nil, err
	}

	ids, err := distinctMentionIDs(request.MentionIds)
	if err != nil {
		return nil, err
	}

	notice := sessionMentionNotice{server: s, id: request.Id, ids: ids}
	notice.PromptAccepted(ctx)

	return &PromptResponse{MentionWarning: notice.warning}, nil
}
