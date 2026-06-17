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
	log     *slog.Logger
	post    func(context.Context, *events.AskUserQuestionRequest) (events.TextConversationTarget, error)
	delete  func(context.Context, events.TextConversationTarget) error
	mu      sync.Mutex
	pending map[string]*askUserQuestionPending
}

func newAskUserQuestionBroker(log *slog.Logger) *askUserQuestionBroker {
	errPost := func(context.Context, *events.AskUserQuestionRequest) (events.TextConversationTarget, error) {
		return events.TextConversationTarget{}, errors.New("primary text connector is not ready")
	}
	errDelete := func(context.Context, events.TextConversationTarget) error { return nil }

	return &askUserQuestionBroker{log: log.With("component", "ask_user_question"), post: errPost, delete: errDelete, pending: map[string]*askUserQuestionPending{}}
}

func (b *askUserQuestionBroker) ask(ctx context.Context, req *events.AskUserQuestionRequest) (events.AskUserQuestionAnswer, error) {
	target, err := b.post(ctx, req)
	if err != nil {
		return events.AskUserQuestionAnswer{}, err
	}

	p := &askUserQuestionPending{req: req, target: target, ch: make(chan events.AskUserQuestionAnswer, 1)}

	b.mu.Lock()
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

func (b *askUserQuestionBroker) answer(id string, answer events.AskUserQuestionAnswer) bool {
	b.mu.Lock()
	p := b.pending[id]
	delete(b.pending, id)
	b.mu.Unlock()

	if p == nil {
		return false
	}

	p.ch <- answer

	return true
}

func (b *askUserQuestionBroker) answerText(source events.Source, target events.TextConversationTarget, text string) bool {
	b.mu.Lock()
	for id, p := range b.pending {
		if p.req.Source == source && p.req.AllowCustom && (source == events.SourceSlack && p.req.SlackReply.ChannelID == target.ChannelID && p.req.SlackReply.ThreadTS == target.ThreadID || source == events.SourceDiscordText && (p.req.DiscordReply.ThreadID == target.ThreadID || p.req.DiscordReply.ChannelID == target.ChannelID)) {
			delete(b.pending, id)
			b.mu.Unlock()

			p.ch <- events.AskUserQuestionAnswer{Custom: text, Source: source}

			return true
		}
	}
	b.mu.Unlock()

	return false
}

func (b *askUserQuestionBroker) cancelUnanswered(ctx context.Context) {
	b.mu.Lock()
	pending := b.pending
	b.pending = map[string]*askUserQuestionPending{}
	b.mu.Unlock()

	for _, p := range pending {
		b.deletePending(ctx, p)
		close(p.ch)
	}
}

func (b *askUserQuestionBroker) deletePending(ctx context.Context, p *askUserQuestionPending) {
	err := b.delete(ctx, p.target)
	if err != nil {
		b.log.Warn("delete unanswered question", "source", p.req.Source, "error", err)
	}
}
