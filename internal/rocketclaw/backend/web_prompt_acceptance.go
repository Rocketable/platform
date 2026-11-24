package backend

import "context"

type webPromptAcceptance interface {
	PromptAccepted(context.Context)
}

type webPromptAcceptanceKey struct{}

func WithWebPromptAcceptance(ctx context.Context, acceptance webPromptAcceptance) context.Context {
	return context.WithValue(ctx, webPromptAcceptanceKey{}, acceptance)
}
