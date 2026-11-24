package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

const webQuestionStep = "/web-question"

const saveWebQuestionStep = `saved AS (
    INSERT INTO steps (conversation_id, key, value) VALUES ($1, $2, $3) ON CONFLICT (conversation_id, key) DO UPDATE SET value = excluded.value
    RETURNING old.key IS NULL AS added
)`

const askWebQuestionSQL = saveWebQuestionStep + `, running AS (
    SELECT id, conversation_id, inbound_json FROM turns WHERE conversation_id = $1 AND phase NOT IN ('waiting', 'done') AND takes_lane
    ORDER BY created_at_unix_ns LIMIT 1
), turn AS (
    SELECT r.id, c.conversation_id, c.kind, r.inbound_json, TRUE AS waking FROM running r CROSS JOIN saved s
    JOIN conversations c ON c.conversation_id = r.conversation_id AND c.listed WHERE s.added AND starts_with($2, r.id || '/')
)` + turnEventsSQL + sessionReadersSQL + markTabsSQL

type QuestionNotPendingError struct{ ConversationID, QuestionID string }

func (e *QuestionNotPendingError) Error() string {
	return fmt.Sprintf("question %q is no longer pending in conversation %q", e.QuestionID, e.ConversationID)
}

type webQuestionKey struct{ conversationID, questionID string }

type webQuestions struct {
	store *SessionService

	mu      sync.Mutex
	waiting map[webQuestionKey]chan protocol.AskUserQuestionAnswer
}

func (q *webQuestions) AskUserQuestion(ctx context.Context, req *protocol.AskUserQuestionRequest) (protocol.AskUserQuestionAnswer, error) {
	if recorded, answered, err := q.store.LoadTurnStep(ctx, req.ConversationID, req.ID+"/answer"); err != nil || answered {
		var answer protocol.AskUserQuestionAnswer
		if err == nil {
			err = json.Unmarshal(recorded, &answer)
		}

		if err != nil {
			return protocol.AskUserQuestionAnswer{}, fmt.Errorf("load recorded Web answer: %w", err)
		}

		return answer, nil
	}

	data, _ := json.Marshal(req)
	if err := q.change(ctx, askWebQuestionSQL, req.ConversationID, req.ID+webQuestionStep, string(removeSessionEntryNUL(data))); err != nil {
		return protocol.AskUserQuestionAnswer{}, err
	}

	key, answers := webQuestionKey{req.ConversationID, req.ID}, make(chan protocol.AskUserQuestionAnswer, 1)

	q.mu.Lock()
	q.waiting[key] = answers
	q.mu.Unlock()

	select {
	case answer := <-answers:
		return answer, nil
	case <-ctx.Done():
		q.mu.Lock()
		_, waiting := q.waiting[key]
		delete(q.waiting, key)
		q.mu.Unlock()

		if !waiting {
			return <-answers, nil
		}

		errWait := fmt.Errorf("wait for human answer: %w", ctx.Err())
		if errors.Is(context.Cause(ctx), protocol.ErrBridgeStopped) {
			return protocol.AskUserQuestionAnswer{}, errWait
		}

		return protocol.AskUserQuestionAnswer{}, errors.Join(errWait, q.change(context.WithoutCancel(ctx), `withdrawn AS (DELETE FROM steps WHERE conversation_id = $1 AND key = $2)`, req.ConversationID, req.ID+webQuestionStep))
	}
}

func (q *webQuestions) change(ctx context.Context, ctes, conversationID, key string, value ...any) error {
	if _, err := execState(ctx, q.store.db, fmt.Sprintf("write turn step %q", key), `WITH `+ctes+` SELECT pg_notify('rocketclaw_transcript_' || md5(current_schema()), json_build_object('conversationId', $1::text, 'revision', pg_current_xact_id()::text)::text)`, append([]any{conversationID, key}, value...)...); err != nil {
		return err
	}

	q.store.ringListing()

	return nil
}

func (r *Runtime) PendingQuestions(ctx context.Context, conversationID string) ([]protocol.AskUserQuestionRequest, error) {
	return queryRows(ctx, r.Sessions.db, `SELECT q.value FROM steps q LEFT JOIN steps a ON a.conversation_id = $1 AND a.key = left(q.key, -length('`+webQuestionStep+`')) || '/answer'
WHERE q.conversation_id = $1 AND q.key LIKE '%`+webQuestionStep+`' AND a.key IS NULL ORDER BY q.key`, "pending Web questions", func(row rowScanner) (protocol.AskUserQuestionRequest, error) {
		var (
			data []byte
			req  protocol.AskUserQuestionRequest
		)

		err := row.Scan(&data)
		if err == nil {
			err = json.Unmarshal(data, &req)
		}

		if err != nil {
			return req, fmt.Errorf("read pending Web question: %w", err)
		}

		return req, nil
	}, conversationID)
}

func (r *Runtime) AnswerQuestion(ctx context.Context, conversationID, questionID string, answer protocol.AskUserQuestionAnswer) error {
	if err := rejectShadowMutation(ctx, r.Sessions.db, conversationID); err != nil {
		return err
	}

	q, key := r.questions, webQuestionKey{conversationID, questionID}

	q.mu.Lock()
	defer q.mu.Unlock()

	answers, waiting := q.waiting[key]
	if !waiting {
		return &QuestionNotPendingError{ConversationID: conversationID, QuestionID: questionID}
	}

	answer.Source = protocol.SourceWeb
	data, _ := json.Marshal(answer)

	if err := q.change(ctx, saveWebQuestionStep, conversationID, questionID+"/answer", string(removeSessionEntryNUL(data))); err != nil {
		return err
	}

	delete(q.waiting, key)

	answers <- answer

	return nil
}
