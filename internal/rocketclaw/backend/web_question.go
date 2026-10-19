package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

// webQuestionStep suffixes the turn step a Web question is saved under.
const webQuestionStep = "/web-question"

const saveWebQuestionStep = `INSERT INTO steps (conversation_id, key, value) VALUES ($1, $2, $3) ON CONFLICT (conversation_id, key) DO UPDATE SET value = excluded.value`

// QuestionNotPendingError reports an answer to a question no Web turn waits on, or one already answered.
type QuestionNotPendingError struct{ ConversationID, QuestionID string }

func (e *QuestionNotPendingError) Error() string {
	return fmt.Sprintf("question %q is no longer pending in conversation %q", e.QuestionID, e.ConversationID)
}

type webQuestionKey struct{ conversationID, questionID string }

// webQuestions asks Web turns' questions. Each is saved as a turn step, which lists it as
// pending, and its turn waits in this process until AnswerQuestion saves the answer.
type webQuestions struct {
	store *SessionService

	mu      sync.Mutex
	waiting map[webQuestionKey]chan protocol.AskUserQuestionAnswer
}

// AskUserQuestion returns the saved answer when a resumed turn asks an answered question again,
// and otherwise saves the question and waits. A turn left for restart keeps its question; any
// other cancellation withdraws it.
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

	data, _ := json.Marshal(req) // Encoding strings, bools, and slices of them cannot fail.
	if err := q.change(ctx, saveWebQuestionStep, req.ConversationID, req.ID+webQuestionStep, string(removeSessionEntryNUL(data))); err != nil {
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

		if !waiting { // An answer won the race and is already sent.
			return <-answers, nil
		}

		errWait := fmt.Errorf("wait for human answer: %w", ctx.Err())
		if errors.Is(context.Cause(ctx), protocol.ErrBridgeStopped) {
			return protocol.AskUserQuestionAnswer{}, errWait
		}

		return protocol.AskUserQuestionAnswer{}, errors.Join(errWait, q.change(context.WithoutCancel(ctx), `DELETE FROM steps WHERE conversation_id = $1 AND key = $2`, req.ConversationID, req.ID+webQuestionStep))
	}
}

// change runs statement, a write of step $2 of conversation $1, and wakes the conversation's Web
// views in the same transaction, since no trigger reports steps whose key holds a slash.
func (q *webQuestions) change(ctx context.Context, statement, conversationID, key string, value ...any) error {
	_, err := execState(ctx, q.store.db, fmt.Sprintf("write turn step %q", key), `WITH changed AS (`+statement+`) SELECT pg_notify('rocketclaw_transcript_' || md5(current_schema()), json_build_object('conversationId', $1::text, 'revision', pg_current_xact_id()::text)::text)`, append([]any{conversationID, key}, value...)...)

	return err
}

// PendingQuestions lists, in key order, the unanswered questions saved by conversationID's Web
// turns. A question stays listed while its turn resumes after a restart.
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

// AnswerQuestion saves the answer to the Web question questionID of conversationID and wakes the
// turn waiting on it. The first answer wins: an answer to a question no Web turn of that
// conversation waits on, which includes one already answered, fails with *QuestionNotPendingError.
func (r *Runtime) AnswerQuestion(ctx context.Context, conversationID, questionID string, answer protocol.AskUserQuestionAnswer) error {
	if err := rejectShadowMutation(ctx, r.Sessions.db, conversationID); err != nil {
		return err
	}

	q, key := r.questions, webQuestionKey{conversationID, questionID}

	// Holding q.mu across the save keeps a second answer from saving or waking anything.
	q.mu.Lock()
	defer q.mu.Unlock()

	answers, waiting := q.waiting[key]
	if !waiting {
		return &QuestionNotPendingError{ConversationID: conversationID, QuestionID: questionID}
	}

	answer.Source = protocol.SourceWeb
	data, _ := json.Marshal(answer) // Encoding strings cannot fail.

	if err := q.change(ctx, saveWebQuestionStep, conversationID, questionID+"/answer", string(removeSessionEntryNUL(data))); err != nil {
		return err
	}

	delete(q.waiting, key)

	answers <- answer

	return nil
}
