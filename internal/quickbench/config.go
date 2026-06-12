package quickbench

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/Rocketable/platform/internal/rocketcode"
	anthropic "github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

type providerConfigFile struct {
	Providers map[string]providerConfig `json:"providers"`
}

type providerConfig struct {
	APIKey  string `json:"apiKey"`
	BaseURL string `json:"baseURL"`
}

var envTemplatePattern = regexp.MustCompile(`\{\{\s*env\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

func loadProviderConfig(path string, models []modelSelector) (rocketcode.Providers, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return rocketcode.Providers{}, fmt.Errorf("read quickbench.json: %w", err)
	}

	var cfg providerConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return rocketcode.Providers{}, fmt.Errorf("parse quickbench.json: %w", err)
	}

	if len(cfg.Providers) == 0 {
		return rocketcode.Providers{}, errors.New("quickbench.json must define providers")
	}

	selected := map[string]bool{}
	for _, model := range models {
		selected[model.Provider] = true
	}

	missing := []string{}
	for name, provider := range cfg.Providers {
		if !selected[name] {
			continue
		}

		provider.APIKey = interpolateEnv(provider.APIKey, &missing)
		provider.BaseURL = interpolateEnv(provider.BaseURL, &missing)
		cfg.Providers[name] = provider
	}

	if len(missing) > 0 {
		return rocketcode.Providers{}, fmt.Errorf("quickbench.json references missing environment variables: %s", strings.Join(missing, ", "))
	}

	var providers rocketcode.Providers
	if selected["openai"] {
		provider, ok := cfg.Providers["openai"]
		if !ok {
			return rocketcode.Providers{}, errors.New("quickbench.json missing selected provider openai")
		}

		apiKey := strings.TrimSpace(provider.APIKey)
		if apiKey == "" {
			return rocketcode.Providers{}, errors.New("quickbench.json provider openai requires apiKey")
		}

		options := []option.RequestOption{option.WithAPIKey(apiKey)}
		if baseURL := strings.TrimSpace(provider.BaseURL); baseURL != "" {
			options = append(options, option.WithBaseURL(baseURL))
		}

		client := openai.NewClient(options...)
		providers.OpenAI = &client
	}

	if selected["anthropic"] {
		provider, ok := cfg.Providers["anthropic"]
		if !ok {
			return rocketcode.Providers{}, errors.New("quickbench.json missing selected provider anthropic")
		}

		apiKey := strings.TrimSpace(provider.APIKey)
		if apiKey == "" {
			return rocketcode.Providers{}, errors.New("quickbench.json provider anthropic requires apiKey")
		}

		options := []anthropicoption.RequestOption{anthropicoption.WithAPIKey(apiKey)}
		if baseURL := strings.TrimSpace(provider.BaseURL); baseURL != "" {
			options = append(options, anthropicoption.WithBaseURL(baseURL))
		}

		client := anthropic.NewClient(options...)
		providers.Anthropic = &client
	}

	return providers, nil
}

func interpolateEnv(input string, missing *[]string) string {
	return envTemplatePattern.ReplaceAllStringFunc(input, func(match string) string {
		name := envTemplatePattern.FindStringSubmatch(match)[1]
		value, ok := os.LookupEnv(name)
		if !ok {
			*missing = append(*missing, name)
		}

		return value
	})
}
