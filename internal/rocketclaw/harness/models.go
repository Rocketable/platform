package harness

import (
	"errors"
	"fmt"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

const defaultOpenAIModel shared.ResponsesModel = "gpt-6-luna"

// ProviderOrigin identifies the provider and API model selected by a resolver.
type ProviderOrigin struct {
	Provider         string
	Model            string
	CompactThreshold int64
	// PromptCacheMarkersRelativePosition, when set, is the negative index of the text
	// piece in the last request message that carries an Anthropic cache_control marker.
	PromptCacheMarkersRelativePosition *int
}

// ModelResolver selects an SDK client and API model for a model selector.
type ModelResolver interface {
	Resolve(model string) (*openai.Client, ProviderOrigin, error)
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
	return o.Provider + "/" + o.Model
}
