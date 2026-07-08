package rocketcode

import (
	"errors"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3/shared"
)

const defaultOpenAIModel shared.ResponsesModel = "gpt-5.5"

type modelRef struct{ apiModel string }

func defaultModelRef() modelRef {
	return modelRef{apiModel: defaultOpenAIModel}
}

func (m modelRef) display() string {
	return m.apiModel
}

func parseModelRef(model string) (modelRef, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return defaultModelRef(), nil
	}

	if after, ok := strings.CutPrefix(model, "openai/"); ok {
		model = after
		if model == "" || strings.Contains(model, "/") {
			return modelRef{}, fmt.Errorf("invalid model %q: expected openai/model", "openai/"+model)
		}

		return modelRef{apiModel: model}, nil
	}

	if strings.Contains(model, "/") {
		return modelRef{}, fmt.Errorf("invalid model %q: expected unprefixed OpenAI model ID", model)
	}

	return modelRef{apiModel: model}, nil
}

func resolveAgentModelRef(model string) (modelRef, error) {
	if strings.TrimSpace(model) == "" {
		return modelRef{}, errors.New("required non-empty string")
	}

	return parseModelRef(model)
}
