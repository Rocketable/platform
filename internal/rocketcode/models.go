package rocketcode

import (
	"fmt"
	"strings"

	openai "github.com/openai/openai-go/v3"
)

const modelProviderOpenAI, modelProviderAnthropic = "openai", "anthropic"

type modelRef struct{ provider, apiModel string }

func defaultModelRef() modelRef {
	return modelRef{provider: modelProviderOpenAI, apiModel: openai.ChatModelGPT5_4}
}

func (m modelRef) display() string { return m.provider + "/" + m.apiModel }

func parseModelRef(model string) (modelRef, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return defaultModelRef(), nil
	}

	provider, apiModel, ok := strings.Cut(model, "/")
	if !ok {
		return modelRef{provider: modelProviderOpenAI, apiModel: model}, nil
	}

	if provider == "" || apiModel == "" {
		return modelRef{}, fmt.Errorf("invalid model %q: expected provider/model", model)
	}

	switch provider {
	case modelProviderOpenAI, modelProviderAnthropic:
		return modelRef{provider: provider, apiModel: apiModel}, nil
	default:
		return modelRef{}, fmt.Errorf("unsupported model provider %q", provider)
	}
}

func parseAgentModelRef(model string, fallback modelRef) (modelRef, error) {
	if strings.TrimSpace(model) == "" {
		return fallback, nil
	}

	return parseModelRef(model)
}
