package backend

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

type SecretRequest struct {
	ID, Name, Reason string

	starter, answer, commands string
	secrets                   *harness.TurnSecretStore
	answered                  chan struct{}
}

func askSecretTool(sessions *SessionService, secrets *harness.TurnSecretStore, msg *protocol.InboundMessage, commands string) harness.Tool {
	return harness.Tool{Name: harness.AskSecretToolName, Resumable: true, TurnBound: true, Description: "Ask the person who started this turn for a secret and wait. name: its environment variable; reason: why.\nThey paste it into a password form, so its value never reaches you. Once saved, " + commands + " for the rest of this turn sees it as $name. name is A-Z, 0-9 and _, ending in _TOKEN, _KEY, _SECRET or _PASSWORD. Asking again for a name replaces its value.", Permission: "rocketclaw", VisibilitySubjects: []string{harness.AskSecretToolName}, Subjects: func(json.RawMessage) ([]string, error) { return []string{harness.AskSecretToolName}, nil }, Parameters: map[string]any{"properties": map[string]any{"name": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string"}}, "required": []string{"name", "reason"}}, Call: func(ctx context.Context, raw json.RawMessage, _ chan<- harness.ChatResponse) (harness.ToolResult, error) {
		ask := &SecretRequest{starter: msg.Metadata[protocol.InboundPrincipalEmailMetadataKey], commands: commands, secrets: secrets, answered: make(chan struct{})}

		if err := json.Unmarshal(raw, ask); err != nil {
			return harness.ToolResult{}, fmt.Errorf("parse secret request: %w", err)
		}

		if err := harness.CheckTurnSecretName(ask.Name); err != nil {
			return harness.ToolResult{}, fmt.Errorf("ask secret: %w", err)
		}

		ask.ID = cmp.Or(harness.ToolCallKey(ctx), rand.Text())
		key := webQuestionKey{msg.ConversationID, ask.ID}

		ctx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)

		if moved, ok := ctx.Value(backgroundMovedKey{}).(context.Context); ok {
			defer context.AfterFunc(moved, func() { cancel(context.Cause(moved)) })()
		}

		sessions.secretsMu.Lock()
		sessions.secrets[key] = ask
		sessions.secretsMu.Unlock()

		errAsk := sessions.change(context.WithoutCancel(ctx), `saved AS (SELECT TRUE AS added)`+markAskingSQL, key.conversationID, key.questionID)
		if errAsk == nil {
			select {
			case <-ask.answered:
			case <-ctx.Done():
			}
		}

		sessions.secretsMu.Lock()
		delete(sessions.secrets, key)

		text := cmp.Or(ask.answer, "No answer: the turn stopped or moved this script to the background while ask_secret waited, so the request for "+ask.Name+" was withdrawn and nobody will answer it; it is not set. Ask in the turn that receives this job's result if you still need it.")
		sessions.secretsMu.Unlock()

		return harness.TextToolResult(text), errors.Join(errAsk, sessions.change(context.WithoutCancel(ctx), `answered AS (SELECT $2::text)`, key.conversationID, key.questionID))
	}}
}

func (s *SessionService) PendingSecrets(conversationID, username string) []SecretRequest {
	s.secretsMu.Lock()
	defer s.secretsMu.Unlock()

	var pending []SecretRequest

	for key, ask := range s.secrets {
		if key.conversationID == conversationID && ask.starter == username {
			pending = append(pending, *ask)
		}
	}

	slices.SortFunc(pending, func(a, b SecretRequest) int { return strings.Compare(a.ID, b.ID) })

	return pending
}

func (s *SessionService) AnswerSecret(conversationID, askID, username string, value []byte, decline bool) error {
	s.secretsMu.Lock()
	defer s.secretsMu.Unlock()

	key := webQuestionKey{conversationID, askID}

	ask, waiting := s.secrets[key]
	switch {
	case !waiting || ask.starter != username:
		return &QuestionNotPendingError{ConversationID: conversationID, QuestionID: askID}
	case decline:
		ask.answer = "Declined: the user chose not to give " + ask.Name + ", so it is not set. Decide without it."
	default:
		if err := ask.secrets.Save(ask.Name, value); err != nil {
			return fmt.Errorf("save turn secret: %w", err)
		}

		ask.answer = fmt.Sprintf("Saved: $%[1]s is set in %[2]s for the rest of this turn only, and its value shows to you as [secret:%[1]s]. Do not print it, save it to disk, or put it in command arguments: use tools that read the variable, stdin, or a header file.", ask.Name, ask.commands)
	}

	delete(s.secrets, key)
	close(ask.answered)

	return nil
}
