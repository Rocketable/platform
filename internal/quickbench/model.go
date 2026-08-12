package quickbench

import (
	"errors"
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
		return modelSelector{}, errors.New("empty selector")
	}

	model, rawQuery, _ := strings.Cut(input, "?")
	if strings.TrimSpace(model) == "" || strings.Contains(model, "/") {
		return modelSelector{}, fmt.Errorf("invalid selector %q: expected unprefixed OpenAI model ID", input)
	}

	selector := modelSelector{Raw: input, Provider: "openai", Model: model}

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
			switch values[0] {
			case "none", "minimal", "low", "medium", "high", "xhigh", "max":
				selector.ReasoningEffort = values[0]
			default:
				return modelSelector{}, errors.New("reasoningEffort must be none, minimal, low, medium, high, xhigh, or max")
			}
		case "verbosity":
			if values[0] != "low" && values[0] != "medium" && values[0] != "high" {
				return modelSelector{}, errors.New("verbosity must be low, medium, or high")
			}

			selector.Verbosity = values[0]
		default:
			return modelSelector{}, fmt.Errorf("unknown option %q", key)
		}
	}

	return selector, nil
}
