package rocketcode

import (
	"fmt"
	"strings"

	openai "github.com/openai/openai-go/v3"
)

type modelRef struct{ apiModel string }

func defaultModelRef() modelRef {
	return modelRef{apiModel: openai.ChatModelGPT5_4}
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

func resolveAgentModelRef(model string, defaultModel modelRef) (modelRef, error) {
	if strings.TrimSpace(model) == "" {
		return defaultModel, nil
	}

	return parseModelRef(model)
}
