package app

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAskUserQuestionBrokerDeletesQuestionBeforeChoiceAnswerReturns(t *testing.T) {
	b := newAskUserQuestionBroker(slog.New(slog.DiscardHandler))
	target := events.TextConversationTarget{ChannelID: "C1", MessageID: "M1", ThreadID: "T1"}
	order := make(chan string, 2)
	b.post = func(context.Context, *events.AskUserQuestionRequest) (events.TextConversationTarget, error) {
		return target, nil
	}
	b.delete = func(_ context.Context, got events.TextConversationTarget) error {
		assert.Equal(t, target, got)

		order <- "delete"

		return nil
	}

	done := make(chan events.AskUserQuestionAnswer, 1)
	errs := make(chan error, 1)

	go func() {
		answer, err := b.ask(t.Context(), &events.AskUserQuestionRequest{ID: "question-1", Source: events.SourceSlack})
		if err != nil {
			errs <- err

			return
		}

		order <- "answer"

		done <- answer
	}()

	require.Eventually(t, func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()

		return b.pending["question-1"] != nil
	}, time.Second, time.Millisecond)

	assert.True(t, b.answer(t.Context(), "question-1", events.AskUserQuestionAnswer{Selected: []string{"yes"}, Source: events.SourceSlack}))
	assert.Equal(t, "delete", <-order)
	assert.Equal(t, "answer", <-order)
	assert.Equal(t, events.AskUserQuestionAnswer{Selected: []string{"yes"}, Source: events.SourceSlack}, <-done)
	assert.Empty(t, errs)
}

func TestAskUserQuestionBrokerDeletesQuestionBeforeTextAnswerReturns(t *testing.T) {
	b := newAskUserQuestionBroker(slog.New(slog.DiscardHandler))
	target := events.TextConversationTarget{ChannelID: "C1", MessageID: "M1", ThreadID: "C1"}
	order := make(chan string, 2)
	b.post = func(context.Context, *events.AskUserQuestionRequest) (events.TextConversationTarget, error) {
		return target, nil
	}
	b.delete = func(_ context.Context, got events.TextConversationTarget) error {
		assert.Equal(t, target, got)

		order <- "delete"

		return nil
	}

	done := make(chan events.AskUserQuestionAnswer, 1)
	errs := make(chan error, 1)

	go func() {
		answer, err := b.ask(t.Context(), &events.AskUserQuestionRequest{ID: "question-1", Source: events.SourceSlack})
		if err != nil {
			errs <- err

			return
		}

		order <- "answer"

		done <- answer
	}()

	require.Eventually(t, func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()

		return b.pending["question-1"] != nil
	}, time.Second, time.Millisecond)

	assert.True(t, b.answer(t.Context(), "question-1", events.AskUserQuestionAnswer{Custom: "free text", Source: events.SourceSlack}))
	assert.Equal(t, "delete", <-order)
	assert.Equal(t, "answer", <-order)
	assert.Equal(t, events.AskUserQuestionAnswer{Custom: "free text", Source: events.SourceSlack}, <-done)
	assert.Empty(t, errs)
}

func TestAskUserQuestionBrokerCancellationDeletesUnansweredQuestion(t *testing.T) {
	b := newAskUserQuestionBroker(slog.New(slog.DiscardHandler))
	target := events.TextConversationTarget{ChannelID: "C1", MessageID: "M1", ThreadID: "T1"}
	deleted := make(chan events.TextConversationTarget, 1)
	b.post = func(context.Context, *events.AskUserQuestionRequest) (events.TextConversationTarget, error) {
		return target, nil
	}
	b.delete = func(deleteCtx context.Context, got events.TextConversationTarget) error {
		assert.NoError(t, deleteCtx.Err())

		deleted <- got

		return nil
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() {
		_, err := b.ask(ctx, &events.AskUserQuestionRequest{ID: "question-1", Source: events.SourceSlack})
		done <- err
	}()

	require.Eventually(t, func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()

		return b.pending["question-1"] != nil
	}, time.Second, time.Millisecond)

	cancel()

	assert.Equal(t, target, <-deleted)
	require.ErrorIs(t, <-done, context.Canceled)
}
