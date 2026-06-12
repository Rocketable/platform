package quickbench

import (
	"fmt"
	"net/url"
	"strings"
)

type modelSelector struct {
	Raw             string
	Provider        string
	Model           string
	ReasoningEffort string
	Verbosity       string
}

func parseModelSelector(input string) (modelSelector, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return modelSelector{}, fmt.Errorf("empty selector")
	}

	path, rawQuery, _ := strings.Cut(input, "?")
	provider, model, ok := strings.Cut(path, "/")
	if !ok || provider == "" || model == "" {
		return modelSelector{}, fmt.Errorf("invalid selector %q: expected provider/model", input)
	}

	if provider != "openai" && provider != "anthropic" {
		return modelSelector{}, fmt.Errorf("unsupported provider %q", provider)
	}

	selector := modelSelector{Raw: input, Provider: provider, Model: model}
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return modelSelector{}, fmt.Errorf("parse query: %w", err)
	}

	for key, values := range query {
		if len(values) != 1 {
			return modelSelector{}, fmt.Errorf("option %q must appear once", key)
		}

		switch key {
		case "reasoningEffort":
			if values[0] != "minimal" && values[0] != "low" && values[0] != "medium" && values[0] != "high" {
				return modelSelector{}, fmt.Errorf("reasoningEffort must be minimal, low, medium, or high")
			}

			selector.ReasoningEffort = values[0]
		case "verbosity":
			if values[0] != "low" && values[0] != "medium" && values[0] != "high" {
				return modelSelector{}, fmt.Errorf("verbosity must be low, medium, or high")
			}

			selector.Verbosity = values[0]
		default:
			return modelSelector{}, fmt.Errorf("unknown option %q", key)
		}
	}

	return selector, nil
}

func (m modelSelector) rocketCodeModel() string {
	return m.Provider + "/" + m.Model
}
