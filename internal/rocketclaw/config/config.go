// Package config loads and validates rocketclaw configuration files.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"
)

// Config is the top-level rocketclaw runtime configuration.
type Config struct {
	Workspace         string                  `json:"workspace"`
	WorkDir           string                  `json:"-"`
	Overlays          []string                `json:"overlays,omitempty"`
	Models            map[string]string       `json:"models,omitempty"`
	Environment       []string                `json:"environment,omitempty"`
	Logging           LoggingConfig           `json:"logging"`
	MCPExternal       MCPExternalConfig       `json:"mcp_external"`
	Slack             SlackConfig             `json:"slack"`
	OpenAI            OpenAIConfig            `json:"openai"`
	Providers         map[string]OpenAIConfig `json:"providers,omitempty"`
	AutoApproverModel string                  `json:"auto_approver_model"`
	Instrumentation   InstrumentationConfig   `json:"instrumentation"`
}

// Provider returns the configuration for name.
func (c *Config) Provider(name string) (OpenAIConfig, bool) {
	if name == "openai" {
		return c.OpenAI, true
	}

	provider, ok := c.Providers[name]

	return provider, ok
}

// DefaultRuntimeDir is the generated runtime directory for rocketclaw configs.
const DefaultRuntimeDir = ".rocketclaw"

// RuntimeDirName returns the selected generated runtime directory name.
func (c *Config) RuntimeDirName() string {
	if strings.TrimSpace(c.WorkDir) != "" {
		return c.WorkDir
	}

	return DefaultRuntimeDir
}

// LoggingConfig controls rocketclaw logging.
type LoggingConfig struct {
	Level string `json:"level"`
}

// MCPExternalConfig configures the persistent external MCP HTTP server.
type MCPExternalConfig struct {
	Enabled    bool   `json:"enabled"`
	ListenAddr string `json:"listen_addr"`
}

// SlackConfig configures Slack channel conversations.
type SlackConfig struct {
	BotToken string               `json:"bot_token"`
	AppToken string               `json:"app_token"`
	Channels []SlackChannelConfig `json:"channels,omitempty"`
}

// SlackChannelConfig configures one Slack channel.
type SlackChannelConfig struct {
	Channel        string   `json:"channel"`
	Agents         []string `json:"agents,omitempty"`
	AllowedUserIDs []string `json:"allowed_user_ids,omitempty"`
}

// OpenAIConfig configures the OpenAI client used by RocketCode.
type OpenAIConfig struct {
	APIKey         string `json:"api_key"`
	APIBaseURL     string `json:"api_base_url"`
	RocketCodeAuth string `json:"rocketcode_auth"`
}

// InstrumentationConfig configures OpenTelemetry/OpenInference tracing.
type InstrumentationConfig struct {
	Enabled           bool   `json:"enabled"`
	CollectorEndpoint string `json:"collector_endpoint"`
	ProjectName       string `json:"project_name"`
	APIKey            string `json:"api_key"`
	HideInputs        bool   `json:"hide_inputs"`
	HideOutputs       bool   `json:"hide_outputs"`
}

// Load reads, normalizes, and validates the rocketclaw configuration file.
func Load(configPath string) (*Config, error) {
	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	return loadConfigData(absPath, data)
}

func loadConfigData(absPath string, data []byte) (*Config, error) {
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config JSON: %w", err)
	}

	configDir := filepath.Dir(absPath)
	if cfg.Workspace == "" {
		cfg.Workspace = configDir
	}

	if !filepath.IsAbs(cfg.Workspace) {
		cfg.Workspace = filepath.Join(configDir, cfg.Workspace)
	}

	workspace, err := filepath.Abs(cfg.Workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

	cfg.Workspace = workspace

	if strings.TrimSpace(cfg.Logging.Level) == "" {
		cfg.Logging.Level = "debug"
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// LoadExternalMCPUsers reads the optional rocketclaw.users.json file next to configPath.
func LoadExternalMCPUsers(configPath string) (map[string]string, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return nil, errors.New("config path is required")
	}

	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}

	usersPath := filepath.Join(filepath.Dir(absPath), "rocketclaw.users.json")

	info, err := os.Stat(usersPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}

		return nil, fmt.Errorf("stat external MCP users file %s: %w", usersPath, err)
	}

	if info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("external MCP users file %s must have mode 0600", usersPath)
	}

	data, err := os.ReadFile(usersPath)
	if err != nil {
		return nil, fmt.Errorf("read external MCP users file %s: %w", usersPath, err)
	}

	var users map[string]string
	if err := json.Unmarshal(data, &users); err != nil {
		return nil, fmt.Errorf("parse external MCP users file %s: %w", usersPath, err)
	}

	if users == nil {
		return nil, fmt.Errorf("parse external MCP users file %s: must be a JSON object", usersPath)
	}

	return users, nil
}

// Validate verifies the configuration is usable.
func (c *Config) Validate() error {
	if c.Workspace == "" {
		return errors.New("workspace is required")
	}

	if err := validateEnvironment(c.Environment); err != nil {
		return err
	}

	c.Overlays = normalizeStrings(c.Overlays)

	for name, model := range c.Models {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(model) == "" {
			return errors.New("models keys and values must not be empty")
		}

		if !strings.Contains(model, "{{") && !strings.Contains(model, "}}") {
			if err := c.validateModelRef(fmt.Sprintf("models[%q]", name), model); err != nil {
				return err
			}
		}
	}

	if err := c.normalizeRocketCodeAuth(); err != nil {
		return err
	}

	c.AutoApproverModel = strings.TrimSpace(c.AutoApproverModel)
	if err := c.validateModelRef("auto_approver_model", c.AutoApproverModel); err != nil {
		return err
	}

	c.Instrumentation.CollectorEndpoint = strings.TrimSpace(c.Instrumentation.CollectorEndpoint)
	c.Instrumentation.ProjectName = strings.TrimSpace(c.Instrumentation.ProjectName)

	c.Instrumentation.APIKey = strings.TrimSpace(c.Instrumentation.APIKey)
	if c.Instrumentation.Enabled && c.Instrumentation.CollectorEndpoint == "" {
		return errors.New("instrumentation.collector_endpoint is required when instrumentation is enabled")
	}

	if c.MCPExternal.Enabled && strings.TrimSpace(c.MCPExternal.ListenAddr) == "" {
		return errors.New("mcp_external.listen_addr is required when mcp_external is enabled")
	}

	if err := c.validateSlack(); err != nil {
		return err
	}

	return nil
}

// RenderAgentModel renders an agent model with the mappings from the loaded config.
func (c *Config) RenderAgentModel(model string) (string, error) {
	if literal := strings.TrimSpace(model); literal != "" && !strings.Contains(model, "{{") && !strings.Contains(model, "}}") {
		if err := c.validateModelRef("model", literal); err != nil {
			return "", err
		}

		return literal, nil
	}

	tmpl, err := template.New("model").Funcs(template.FuncMap{
		"model": func(name string) (string, error) {
			model, ok := c.Models[name]
			if !ok {
				return "", fmt.Errorf("model %q is not configured", name)
			}

			return model, nil
		},
	}).Parse(model)
	if err != nil {
		return "", fmt.Errorf("parse model template: %w", err)
	}

	var rendered strings.Builder
	if err := tmpl.Execute(&rendered, nil); err != nil {
		return "", fmt.Errorf("execute model template: %w", err)
	}

	resolved := strings.TrimSpace(rendered.String())
	if resolved == "" {
		return "", errors.New("model template returned an empty model")
	}

	if strings.Contains(resolved, "{{") || strings.Contains(resolved, "}}") {
		return "", errors.New("model template returned another template")
	}

	if err := c.validateModelRef("model", resolved); err != nil {
		return "", err
	}

	return resolved, nil
}

func (c *Config) validateModelRef(field, model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}

	providerID, apiModel, qualified := strings.Cut(model, "/")
	if !qualified {
		return nil
	}

	if providerID == "" {
		return fmt.Errorf("%s: invalid model %q: provider is required", field, model)
	}

	if apiModel == "" {
		return fmt.Errorf("%s: invalid model %q: model is required", field, model)
	}

	if _, ok := c.Provider(providerID); !ok {
		return fmt.Errorf("%s: invalid model %q: provider %q is not configured", field, model, providerID)
	}

	return nil
}

func (c *Config) normalizeRocketCodeAuth() error {
	for name := range c.Providers {
		if name == "" || name != strings.TrimSpace(name) || name == "openai" || name == "." || !fs.ValidPath(name) || strings.Contains(name, "/") {
			return fmt.Errorf("provider name %q is invalid", name)
		}
	}

	providers := map[string]OpenAIConfig{"openai": c.OpenAI}
	maps.Copy(providers, c.Providers)

	for name, provider := range providers {
		field := "openai"
		if name != "openai" {
			field = fmt.Sprintf("providers[%q]", name)
		}

		switch strings.TrimSpace(provider.RocketCodeAuth) {
		case "", "api_key":
			provider.RocketCodeAuth = "api_key"
		case "chatgpt":
			provider.RocketCodeAuth = "chatgpt"
		default:
			return fmt.Errorf("%s.rocketcode_auth must be api_key or chatgpt", field)
		}

		if provider.RocketCodeAuth == "api_key" && strings.TrimSpace(provider.APIKey) == "" {
			return fmt.Errorf("%s.api_key is required when %s.rocketcode_auth is api_key", field, field)
		}

		provider.APIBaseURL = strings.TrimSpace(provider.APIBaseURL)
		if provider.APIBaseURL != "" {
			baseURL, err := url.Parse(provider.APIBaseURL)
			if err != nil || baseURL.Host == "" || baseURL.Scheme != "http" && baseURL.Scheme != "https" || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
				return fmt.Errorf("%s.api_base_url must be an absolute HTTP(S) URL without userinfo, query, or fragment", field)
			}

			provider.APIBaseURL = strings.TrimRight(provider.APIBaseURL, "/")
		}

		if name == "openai" {
			c.OpenAI = provider
		} else {
			c.Providers[name] = provider
		}
	}

	return nil
}

func normalizeStringList(values []string) []string {
	normalized, unique := normalizeStrings(values), []string{}
	for _, value := range normalized {
		if !slices.Contains(unique, value) {
			unique = append(unique, value)
		}
	}

	return unique
}

func (c *Config) validateSlack() error {
	for _, field := range [...]struct{ value, message string }{{c.Slack.BotToken, "slack.bot_token is required"}, {c.Slack.AppToken, "slack.app_token is required"}} {
		if strings.TrimSpace(field.value) == "" {
			return errors.New(field.message)
		}
	}

	c.Slack.Channels = normalizeSlackChannels(c.Slack.Channels)
	if len(c.Slack.Channels) == 0 {
		return errors.New("slack.channels is required")
	}

	for _, channel := range c.Slack.Channels {
		if len(channel.Agents) == 0 {
			return errors.New("slack.channels[].agents is required")
		}

		if len(channel.AllowedUserIDs) == 0 {
			return errors.New("slack.channels[].allowed_user_ids is required")
		}
	}

	return nil
}

func normalizeSlackChannels(channels []SlackChannelConfig) []SlackChannelConfig {
	normalized := make([]SlackChannelConfig, 0, len(channels))
	for _, channel := range channels {
		channel.Channel = normalizeSlackChannel(channel.Channel)
		channel.Agents = normalizeStringList(channel.Agents)

		channel.AllowedUserIDs = normalizeStringList(channel.AllowedUserIDs)
		if channel.Channel == "" {
			continue
		}

		normalized = append(normalized, channel)
	}

	return normalized
}

func normalizeSlackChannel(channel string) string {
	channel = strings.TrimSpace(channel)
	if channel != "" && !strings.HasPrefix(channel, "#") {
		channel = "#" + channel
	}

	return channel
}

func validateEnvironment(environment []string) error {
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		switch {
		case !ok:
			return fmt.Errorf("environment entry %q must be in KEY=value form", entry)
		case strings.TrimSpace(key) == "":
			return errors.New("environment keys must not be empty")
		case strings.ContainsRune(key, '\x00'):
			return fmt.Errorf("environment key %q must not contain NUL", key)
		case strings.ContainsRune(value, '\x00'):
			return fmt.Errorf("environment value for %q must not contain NUL", key)
		}
	}

	return nil
}

func normalizeStrings(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			normalized = append(normalized, value)
		}
	}

	return normalized
}
