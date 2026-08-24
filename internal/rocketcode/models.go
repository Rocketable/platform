package rocketcode

import (
	"errors"
	"fmt"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

const defaultOpenAIModel shared.ResponsesModel = "gpt-5.5"

type modelRef struct{ apiModel string }

// ProviderOrigin identifies the provider and API model selected by a resolver.
type ProviderOrigin struct {
	Provider         string
	Model            string
	CompactThreshold int64
}

// ModelResolver selects an SDK client and API model for a model selector.
type ModelResolver interface {
	Resolve(model string) (*openai.Client, ProviderOrigin, error)
}

type openAIModelResolver struct {
	client *openai.Client
}

func (r openAIModelResolver) Resolve(model string) (*openai.Client, ProviderOrigin, error) {
	ref, err := resolveAgentModelRef(model)
	if err != nil {
		return nil, ProviderOrigin{}, err
	}

	return r.client, ProviderOrigin{Provider: "openai", Model: ref.apiModel}, nil
}

func resolveModel(resolver ModelResolver, model string) (*openai.Client, ProviderOrigin, error) {
	if strings.TrimSpace(model) == "" {
		return nil, ProviderOrigin{}, errors.New("required non-empty string")
	}

	client, origin, err := resolver.Resolve(model)
	if err != nil {
		return nil, ProviderOrigin{}, fmt.Errorf("resolve model %q: %w", model, err)
	}

	if client == nil {
		return nil, ProviderOrigin{}, errors.New("resolved client is required")
	}

	if strings.TrimSpace(origin.Provider) == "" {
		return nil, ProviderOrigin{}, errors.New("resolved provider is required")
	}

	if strings.TrimSpace(origin.Model) == "" {
		return nil, ProviderOrigin{}, errors.New("resolved model is required")
	}

	return client, origin, nil
}

func (o ProviderOrigin) displayModel() string {
	if o.Provider == "openai" {
		return o.Model
	}

	return o.Provider + "/" + o.Model
}

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
