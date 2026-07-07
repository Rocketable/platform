package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/Rocketable/platform/internal/rocketclaw/events"
)

type askUserQuestionPending struct {
	req    *events.AskUserQuestionRequest
	target events.TextConversationTarget
	ch     chan events.AskUserQuestionAnswer
}

type askUserQuestionBroker struct {
	log      *slog.Logger
	post     func(context.Context, *events.AskUserQuestionRequest) (events.TextConversationTarget, error)
	delete   func(context.Context, events.TextConversationTarget) error
	mu       sync.Mutex
	draining bool
	pending  map[string]*askUserQuestionPending
}

const askUserQuestionDrainText = "timed out while waiting for human partner answer"

func newAskUserQuestionBroker(log *slog.Logger) *askUserQuestionBroker {
	errPost := func(context.Context, *events.AskUserQuestionRequest) (events.TextConversationTarget, error) {
		return events.TextConversationTarget{}, errors.New("primary text connector is not ready")
	}
	errDelete := func(context.Context, events.TextConversationTarget) error { return nil }

	return &askUserQuestionBroker{log: log.With("component", "ask_user_question"), post: errPost, delete: errDelete, pending: map[string]*askUserQuestionPending{}}
}

func (b *askUserQuestionBroker) ask(ctx context.Context, req *events.AskUserQuestionRequest) (events.AskUserQuestionAnswer, error) {
	b.mu.Lock()
	if b.draining {
		b.mu.Unlock()

		return events.AskUserQuestionAnswer{Custom: askUserQuestionDrainText, Source: events.SourceSlack}, nil
	}

	target, err := b.post(ctx, req)
	if err != nil {
		b.mu.Unlock()

		return events.AskUserQuestionAnswer{}, err
	}

	p := &askUserQuestionPending{req: req, target: target, ch: make(chan events.AskUserQuestionAnswer, 1)}
	b.pending[req.ID] = p
	b.mu.Unlock()

	select {
	case answer, ok := <-p.ch:
		if !ok {
			return events.AskUserQuestionAnswer{}, errors.New("ask_user_question canceled")
		}

		return answer, nil
	case <-ctx.Done():
		b.mu.Lock()
		delete(b.pending, req.ID)
		b.mu.Unlock()
		b.deletePending(ctx, p)

		return events.AskUserQuestionAnswer{}, fmt.Errorf("wait for human answer: %w", ctx.Err())
	}
}

func (b *askUserQuestionBroker) answer(ctx context.Context, id string, answer events.AskUserQuestionAnswer) bool {
	b.mu.Lock()
	p := b.pending[id]
	delete(b.pending, id)
	b.mu.Unlock()

	if p == nil {
		return false
	}

	b.deletePending(ctx, p)

	p.ch <- answer

	return true
}

func (b *askUserQuestionBroker) timeoutUnanswered(ctx context.Context) {
	b.mu.Lock()
	pending := b.pending
	b.pending = map[string]*askUserQuestionPending{}
	b.draining = true
	b.mu.Unlock()

	for _, p := range pending {
		b.deletePending(ctx, p)

		p.ch <- events.AskUserQuestionAnswer{Custom: askUserQuestionDrainText, Source: events.SourceSlack}
	}
}

func (b *askUserQuestionBroker) deletePending(ctx context.Context, p *askUserQuestionPending) {
	err := b.delete(ctx, p.target)
	if err != nil {
		b.log.Warn("delete question", "source", p.req.Source, "error", err)
	}
}
