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
	"unicode"
)

// Config is the top-level rocketclaw runtime configuration.
type Config struct {
	Workspace          string                `json:"workspace"`
	WorkDir            string                `json:"-"`
	Overlays           []string              `json:"overlays,omitempty"`
	Environment        []string              `json:"environment,omitempty"`
	EmergencySafeWords []string              `json:"emergency_safe_words,omitempty"`
	ThreadAgents       ThreadAgents          `json:"thread_agents,omitempty"`
	Logging            LoggingConfig         `json:"logging"`
	MCPExternal        MCPExternalConfig     `json:"mcp_external"`
	Slack              SlackConfig           `json:"slack"`
	OpenAI             OpenAIConfig          `json:"openai"`
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

// ThreadAgent configures one Slack emoji prefix thread target.
type ThreadAgent struct {
	Agent   string `json:"agent"`
	PreSeed bool   `json:"pre_seed"`
}

// ThreadAgents maps Slack emoji prefixes to thread routing config.
type ThreadAgents map[string]ThreadAgent

// LoggingConfig controls rocketclaw logging.
type LoggingConfig struct {
	Level string `json:"level"`
}

// MCPExternalConfig configures the persistent external MCP HTTP server.
type MCPExternalConfig struct {
	Enabled    bool   `json:"enabled"`
	ListenAddr string `json:"listen_addr"`
}

// SlackConfig configures the Slack DM connector.
type SlackConfig struct {
	Enabled     bool             `json:"enabled"`
	BotToken    string           `json:"bot_token"`
	AppToken    string           `json:"app_token"`
	Room        string           `json:"room"`
	HumanUserID string           `json:"human_user_id"`
	SocialMode  TextSocialConfig `json:"social_mode"`
}

// TextSocialConfig configures mention-triggered primary text channel conversations.
type TextSocialConfig struct {
	Enabled         bool                      `json:"enabled"`
	Channels        []TextSocialChannelConfig `json:"channels,omitempty"`
	ContextMessages int                       `json:"context_messages"`
}

// TextSocialChannelConfig configures one primary text social-mode channel.
type TextSocialChannelConfig struct {
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

	if cfg.Workspace, err = filepath.Abs(cfg.Workspace); err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

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

// Validate verifies the configuration is usable for the enabled connectors.
func (c *Config) Validate() error {
	if !c.Slack.Enabled && !c.MCPExternal.Enabled {
		return errors.New("enable at least one connector or mcp_external")
	}

	if c.Workspace == "" {
		return errors.New("workspace is required")
	}

	if err := validateEnvironment(c.Environment); err != nil {
		return err
	}

	c.Overlays = normalizeStrings(c.Overlays)

	threadAgents, err := normalizeThreadAgents(c.ThreadAgents)
	if err != nil {
		return err
	}

	c.EmergencySafeWords = normalizeEmergencySafeWords(c.EmergencySafeWords)

	c.ThreadAgents = threadAgents
	if len(c.ThreadAgents) == 0 {
		c.ThreadAgents = ThreadAgents{":thread:": {Agent: "main", PreSeed: false}, ":twisted_rightward_arrows:": {Agent: "main", PreSeed: true}}
	}

	if err := c.normalizeRocketCodeAuth(); err != nil {
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
	if !c.Slack.Enabled {
		return nil
	}

	for _, field := range [...]struct{ value, message string }{{c.Slack.BotToken, "slack.bot_token is required when slack is enabled"}, {c.Slack.AppToken, "slack.app_token is required when slack is enabled"}, {c.Slack.Room, "slack.room is required when slack is enabled"}, {c.Slack.HumanUserID, "slack.human_user_id is required when slack is enabled"}} {
		if strings.TrimSpace(field.value) == "" {
			return errors.New(field.message)
		}
	}

	c.Slack.SocialMode.Channels = normalizeTextSocialChannels(c.Slack.SocialMode.Channels, normalizeSlackSocialChannel)

	return validateTextSocial("slack", &c.Slack.SocialMode)
}

func validateTextSocial(label string, social *TextSocialConfig) error {
	if !social.Enabled {
		return nil
	}

	for _, channel := range social.Channels {
		if len(channel.Agents) == 0 {
			return fmt.Errorf("%s.social_mode.channels[].agents is required when %s social mode is enabled", label, label)
		}

		if len(channel.AllowedUserIDs) == 0 {
			return fmt.Errorf("%s.social_mode.channels[].allowed_user_ids is required when %s social mode is enabled", label, label)
		}
	}

	if social.ContextMessages < 0 {
		return fmt.Errorf("%s.social_mode.context_messages must be zero or greater", label)
	}

	if social.ContextMessages == 0 {
		social.ContextMessages = 10
	}

	return nil
}

func normalizeTextSocialChannels(channels []TextSocialChannelConfig, normalizeChannel func(string) string) []TextSocialChannelConfig {
	normalized := make([]TextSocialChannelConfig, 0, len(channels))
	for _, channel := range channels {
		channel.Channel = normalizeChannel(channel.Channel)
		channel.Agents = normalizeStringList(channel.Agents)

		channel.AllowedUserIDs = normalizeStringList(channel.AllowedUserIDs)
		if channel.Channel == "" {
			continue
		}

		normalized = append(normalized, channel)
	}

	return normalized
}

func normalizeSlackSocialChannel(channel string) string {
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

func normalizeThreadAgents(threadAgents ThreadAgents) (ThreadAgents, error) {
	if len(threadAgents) == 0 {
		return nil, nil
	}

	normalized := make(ThreadAgents, len(threadAgents))

	seen := make(map[string]string, len(threadAgents))
	for prefix, entry := range threadAgents {
		rawPrefix := prefix
		prefix = strings.TrimSpace(prefix)

		entry.Agent = strings.TrimSpace(entry.Agent)
		if prefix == "" || entry.Agent == "" {
			continue
		}

		if previous, ok := seen[prefix]; ok {
			return nil, fmt.Errorf("thread_agents prefix %q duplicates normalized prefix from %q", rawPrefix, previous)
		}

		seen[prefix] = rawPrefix
		normalized[prefix] = entry
	}

	if len(normalized) == 0 {
		return nil, nil
	}

	return normalized, nil
}
