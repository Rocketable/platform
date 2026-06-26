package openresponsesd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
)

const maxConfigBytes = 1 << 20

type config struct {
	Addr            string                    `json:"addr"`
	Auth            authConfig                `json:"auth"`
	DefaultProvider string                    `json:"default_provider"`
	ModelRoutes     []modelRoute              `json:"model_routes"`
	Providers       map[string]providerConfig `json:"providers"`
	State           stateConfig               `json:"state"`
}

type authConfig struct {
	Tokens []string `json:"tokens"`
}

type modelRoute struct {
	Match       string `json:"match"`
	Provider    string `json:"provider"`
	StripPrefix string `json:"strip_prefix"`
}

type providerConfig struct {
	Type             string   `json:"type"`
	APIKey           string   `json:"api_key"`
	APIKeyEnv        string   `json:"api_key_env"`
	BaseURL          string   `json:"base_url"`
	AnthropicVersion string   `json:"anthropic_version"`
	Models           []string `json:"models"`
}

type stateConfig struct {
	Mode         string `json:"mode"`
	MaxResponses int    `json:"max_responses"`
	TTLSeconds   int    `json:"ttl_seconds"`
}

func loadConfigFile(path string) (config, error) {
	file, err := os.Open(path)
	if err != nil {
		return config{}, fmt.Errorf("read config %s: %w", path, err)
	}

	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	errClose := file.Close()
	if err != nil {
		return config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	if errClose != nil {
		return config{}, fmt.Errorf("close config %s: %w", path, errClose)
	}

	if len(data) > maxConfigBytes {
		return config{}, fmt.Errorf("read config %s: exceeds %d bytes", path, maxConfigBytes)
	}

	var cfg config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return config{}, fmt.Errorf("parse config %s: %w", path, err)
	}

	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return config{}, fmt.Errorf("parse config %s: trailing JSON content", path)
	}

	return cfg, nil
}

func (cfg *config) validate(getenv func(string) string) error {
	if strings.TrimSpace(cfg.Addr) == "" {
		cfg.Addr = defaultAddr
	}

	for i := range cfg.Auth.Tokens {
		cfg.Auth.Tokens[i] = strings.TrimSpace(cfg.Auth.Tokens[i])
		if cfg.Auth.Tokens[i] == "" {
			return fmt.Errorf("auth.tokens[%d] must not be empty", i)
		}
	}

	cfg.DefaultProvider = strings.TrimSpace(cfg.DefaultProvider)
	if cfg.DefaultProvider == "" && len(cfg.ModelRoutes) == 0 {
		return errors.New("default_provider or model_routes is required")
	}

	if len(cfg.Providers) == 0 {
		return errors.New("providers is required")
	}

	if cfg.DefaultProvider != "" {
		if _, ok := cfg.Providers[cfg.DefaultProvider]; !ok {
			return fmt.Errorf("default_provider %q is not defined in providers", cfg.DefaultProvider)
		}
	}

	for i := range cfg.ModelRoutes {
		route := &cfg.ModelRoutes[i]
		route.Match = strings.TrimSpace(route.Match)
		route.Provider = strings.TrimSpace(route.Provider)
		if route.Match == "" {
			return fmt.Errorf("model_routes[%d].match is required", i)
		}

		if route.Provider == "" {
			return fmt.Errorf("model_routes[%d].provider is required", i)
		}

		if _, ok := cfg.Providers[route.Provider]; !ok {
			return fmt.Errorf("model_routes[%d].provider %q is not defined in providers", i, route.Provider)
		}
	}

	for _, name := range slices.Sorted(maps.Keys(cfg.Providers)) {
		provider := cfg.Providers[name]
		if err := validateProvider(name, &provider, getenv); err != nil {
			return err
		}

		cfg.Providers[name] = provider
	}

	if strings.TrimSpace(cfg.State.Mode) == "" {
		cfg.State.Mode = "memory"
	}

	if cfg.State.Mode != "memory" {
		return fmt.Errorf("state.mode %q is unsupported; expected memory", cfg.State.Mode)
	}
	if cfg.State.MaxResponses < 0 {
		return errors.New("state.max_responses must not be negative")
	}
	if cfg.State.TTLSeconds < 0 {
		return errors.New("state.ttl_seconds must not be negative")
	}

	return nil
}

func validateProvider(name string, provider *providerConfig, getenv func(string) string) error {
	provider.Type = strings.TrimSpace(provider.Type)
	provider.APIKeyEnv = strings.TrimSpace(provider.APIKeyEnv)
	provider.BaseURL = strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")

	switch provider.Type {
	case "openai_responses", "openai_chat_completions", "anthropic_messages":
	case "":
		return fmt.Errorf("providers.%s.type is required", name)
	default:
		return fmt.Errorf("providers.%s.type %q is unsupported", name, provider.Type)
	}

	if provider.BaseURL == "" {
		return fmt.Errorf("providers.%s.base_url is required", name)
	}

	if provider.APIKeyEnv != "" {
		switch provider.APIKeyEnv {
		case "OPENRESPONSESD_OPENAI_API_KEY", "OPENRESPONSESD_ANTHROPIC_API_KEY":
		default:
			return fmt.Errorf("providers.%s.api_key_env %q is not documented", name, provider.APIKeyEnv)
		}

		provider.APIKey = getenv(provider.APIKeyEnv)
		if strings.TrimSpace(provider.APIKey) == "" {
			return fmt.Errorf("providers.%s.api_key_env %q is not set", name, provider.APIKeyEnv)
		}
	}

	if strings.TrimSpace(provider.APIKey) == "" {
		return fmt.Errorf("providers.%s.api_key or api_key_env is required", name)
	}

	return nil
}
