package rocketcode

import (
	"errors"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3/shared"
)

const defaultOpenAIModel shared.ResponsesModel = "gpt-5.5"
const defaultProviderID = "openai"

type modelRef struct {
	providerID string
	apiModel   string
}

func defaultModelRef() modelRef {
	return modelRef{providerID: defaultProviderID, apiModel: defaultOpenAIModel}
}

func (m modelRef) display() string {
	if m.providerID != defaultProviderID {
		return m.providerID + "/" + m.apiModel
	}

	return m.apiModel
}

func parseModelRef(model string) (modelRef, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return defaultModelRef(), nil
	}

	providerID, apiModel, qualified := strings.Cut(model, "/")
	if !qualified {
		return modelRef{providerID: defaultProviderID, apiModel: model}, nil
	}

	if providerID == "" {
		return modelRef{}, fmt.Errorf("invalid model %q: provider is required", model)
	}

	if apiModel == "" {
		return modelRef{}, fmt.Errorf("invalid model %q: model is required", model)
	}

	return modelRef{providerID: providerID, apiModel: apiModel}, nil
}

func resolveAgentModelRef(model string) (modelRef, error) {
	if strings.TrimSpace(model) == "" {
		return modelRef{}, errors.New("required non-empty string")
	}

	return parseModelRef(model)
}
