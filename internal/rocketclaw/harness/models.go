package harness

import (
	"errors"
	"fmt"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

const defaultOpenAIModel shared.ResponsesModel = "gpt-6-luna"

type ProviderOrigin struct {
	Provider                           string
	Model                              string
	CompactThreshold                   int64
	PromptCacheMarkersRelativePosition *int
}

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

	return client, origin, nil
}

func (o ProviderOrigin) displayModel() string {
	return o.Provider + "/" + o.Model
}
