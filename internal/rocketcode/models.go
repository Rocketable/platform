package rocketcode

import (
	"errors"
	"fmt"
	"strings"

	openai "github.com/openai/openai-go/v3"
)

const modelProviderOpenAI, modelProviderAnthropic, modelProviderOpenAICompatible = "openai", "anthropic", "openai-compatible"

type modelRef struct{ provider, compatibleProvider, apiModel string }

func defaultModelRef() modelRef {
	return modelRef{provider: modelProviderOpenAI, apiModel: openai.ChatModelGPT5_4}
}

func (m modelRef) display() string {
	if m.provider == modelProviderOpenAICompatible {
		return m.provider + "/" + m.compatibleProvider + "/" + m.apiModel
	}

	return m.provider + "/" + m.apiModel
}

func parseModelRef(model string) (modelRef, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return defaultModelRef(), nil
	}

	provider, rest, ok := strings.Cut(model, "/")
	if !ok {
		return modelRef{}, fmt.Errorf("invalid model %q: expected provider/model", model)
	}

	if provider == "" || rest == "" {
		return modelRef{}, fmt.Errorf("invalid model %q: expected provider/model", model)
	}

	switch provider {
	case modelProviderOpenAI, modelProviderAnthropic:
		if strings.Contains(rest, "/") {
			return modelRef{}, fmt.Errorf("invalid model %q: expected provider/model", model)
		}

		return modelRef{provider: provider, apiModel: rest}, nil
	case modelProviderOpenAICompatible:
		compatibleProvider, apiModel, ok := strings.Cut(rest, "/")
		if !ok || compatibleProvider == "" || apiModel == "" || strings.Contains(apiModel, "/") {
			return modelRef{}, fmt.Errorf("invalid model %q: expected openai-compatible/provider/model", model)
		}

		return modelRef{provider: provider, compatibleProvider: compatibleProvider, apiModel: apiModel}, nil
	default:
		return modelRef{}, fmt.Errorf("unsupported model provider %q", provider)
	}
}

func parseAgentModelRef(model string) (modelRef, error) {
	if strings.TrimSpace(model) == "" {
		return modelRef{}, errors.New("model is required")
	}

	return parseModelRef(model)
}
