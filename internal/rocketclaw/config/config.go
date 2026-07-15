// Package config loads and validates rocketclaw configuration files.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"
	"unicode"
)

// Config is the top-level rocketclaw runtime configuration.
type Config struct {
	Workspace          string                `json:"workspace"`
	WorkDir            string                `json:"-"`
	Overlays           []string              `json:"overlays,omitempty"`
	Models             map[string]string     `json:"models,omitempty"`
	Environment        []string              `json:"environment,omitempty"`
	EmergencySafeWords []string              `json:"emergency_safe_words,omitempty"`
	Logging            LoggingConfig         `json:"logging"`
	MCPExternal        MCPExternalConfig     `json:"mcp_external"`
	Slack              SlackConfig           `json:"slack"`
	OpenAI             OpenAIConfig          `json:"openai"`
	AutoApproverModel  string                `json:"auto_approver_model"`
	Instrumentation    InstrumentationConfig `json:"instrumentation"`
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

// MigrateSlackConfig atomically promotes the prior Slack channel shape in configPath.
func MigrateSlackConfig(configPath string) (bool, error) {
	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return false, fmt.Errorf("resolve config path: %w", err)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return false, fmt.Errorf("read config for migration: %w", err)
	}

	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return false, fmt.Errorf("parse config JSON for migration: %w", err)
	}

	if len(document["slack"]) == 0 {
		return false, nil
	}

	var slack map[string]json.RawMessage
	if err := json.Unmarshal(document["slack"], &slack); err != nil {
		return false, fmt.Errorf("parse Slack config for migration: %w", err)
	}

	legacyKeys := [...]string{"enabled", "human_user_id", "room", "social_mode"}
	migrate := false

	for _, key := range legacyKeys {
		_, migrateKey := slack[key]
		migrate = migrate || migrateKey
	}

	if !migrate {
		return false, nil
	}

	if _, canonical := slack["channels"]; !canonical {
		var socialMode map[string]json.RawMessage
		if raw := slack["social_mode"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &socialMode); err != nil {
				return false, fmt.Errorf("parse Slack social mode for migration: %w", err)
			}
		}

		if channels := socialMode["channels"]; len(channels) > 0 {
			slack["channels"] = channels
		}
	}

	for _, key := range legacyKeys {
		delete(slack, key)
	}

	slackData, err := json.Marshal(slack)
	if err != nil {
		return false, fmt.Errorf("encode migrated Slack config: %w", err)
	}

	document["slack"] = slackData

	migrated, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return false, fmt.Errorf("encode migrated config: %w", err)
	}

	migrated = append(migrated, '\n')

	if _, err := loadConfigData(absPath, migrated); err != nil {
		return false, fmt.Errorf("validate migrated config: %w", err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return false, fmt.Errorf("stat config for migration: %w", err)
	}

	if err := os.WriteFile(absPath, migrated, info.Mode().Perm()); err != nil {
		return false, fmt.Errorf("write migrated config: %w", err)
	}

	if err := os.Chmod(absPath, info.Mode().Perm()); err != nil {
		return false, fmt.Errorf("restore migrated config mode: %w", err)
	}

	return true, nil
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
	}

	c.EmergencySafeWords = normalizeEmergencySafeWords(c.EmergencySafeWords)

	if err := c.normalizeRocketCodeAuth(); err != nil {
		return err
	}

	var err error

	c.AutoApproverModel, err = normalizeOpenAIModel("auto_approver_model", c.AutoApproverModel)
	if err != nil {
		return err
	}

	if c.OpenAI.RocketCodeAuth == "api_key" && strings.TrimSpace(c.OpenAI.APIKey) == "" {
		return errors.New("openai.api_key is required when openai.rocketcode_auth is api_key")
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

	return resolved, nil
}

func normalizeOpenAIModel(field, model string) (string, error) {
	model = strings.TrimSpace(model)
	if after, ok := strings.CutPrefix(model, "openai/"); ok {
		if after == "" || strings.Contains(after, "/") {
			return "", fmt.Errorf("%s: invalid model %q: expected openai/model", field, model)
		}

		return after, nil
	}

	if strings.Contains(model, "/") {
		return "", fmt.Errorf("%s: invalid model %q: expected unprefixed OpenAI model ID", field, model)
	}

	return model, nil
}

func (c *Config) normalizeRocketCodeAuth() error {
	switch strings.TrimSpace(c.OpenAI.RocketCodeAuth) {
	case "", "api_key":
		c.OpenAI.RocketCodeAuth = "api_key"
	case "chatgpt":
	default:
		return errors.New("openai.rocketcode_auth must be api_key or chatgpt")
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

func normalizeEmergencySafeWords(words []string) []string {
	if len(words) == 0 {
		return nil
	}

	normalized := make([]string, 0, len(words))

	seen := make(map[string]struct{}, len(words))
	for _, word := range words {
		word = strings.TrimSpace(word)
		if word == "" {
			continue
		}

		var b strings.Builder
		b.Grow(len(word))

		for _, r := range word {
			switch {
			case unicode.IsLetter(r):
				b.WriteRune(unicode.ToLower(r))
			case unicode.IsDigit(r):
				b.WriteRune(r)
			}
		}

		token := b.String()
		if _, ok := seen[token]; token != "" && !ok {
			seen[token] = struct{}{}
			normalized = append(normalized, token)
		}
	}

	return normalized
}
