// Package config loads and validates rocketclaw configuration files.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Config is the top-level rocketclaw runtime configuration.
type Config struct {
	Workspace                                string             `json:"workspace"`
	WorkDir                                  string             `json:"-"`
	Overlays                                 []string           `json:"overlays,omitempty"`
	Environment                              []string           `json:"environment,omitempty"`
	EmergencySafeWords                       []string           `json:"emergency_safe_words,omitempty"`
	ThreadAgents                             ThreadAgents       `json:"thread_agents,omitempty"`
	MinimumWaitAfterHumanInteraction         string             `json:"minimum_wait_after_human_interaction"`
	MinimumWaitAfterHumanInteractionDuration time.Duration      `json:"-"`
	Logging                                  LoggingConfig      `json:"logging"`
	DiscordVoice                             DiscordVoiceConfig `json:"discord_voice"`
	DiscordText                              DiscordTextConfig  `json:"discord_text"`
	MCPExternal                              MCPExternalConfig  `json:"mcp_external"`
	WebUI                                    WebUIConfig        `json:"web_ui"`
	Slack                                    SlackConfig        `json:"slack"`
	OpenAI                                   OpenAIConfig       `json:"openai"`
	Anthropic                                AnthropicConfig    `json:"anthropic"`
}

// DefaultWorkDir is the generated runtime directory for rocketclaw configs.
const DefaultWorkDir = ".rocketclaw"

// WorkDirName returns the selected generated runtime directory name.
func (c *Config) WorkDirName() string {
	if strings.TrimSpace(c.WorkDir) != "" {
		return c.WorkDir
	}

	return DefaultWorkDir
}

// ThreadAgent configures one Slack emoji prefix thread target.
type ThreadAgent struct {
	Agent   string `json:"agent"`
	PreSeed bool   `json:"pre_seed"`
}

// ThreadAgents maps Slack emoji prefixes to thread routing config.
type ThreadAgents map[string]ThreadAgent

// DefaultWebUIListenAddr is the baseline browser voice-mode listener.
const DefaultWebUIListenAddr = "0.0.0.0:8766"

// LoggingConfig controls rocketclaw logging.
type LoggingConfig struct {
	Level string `json:"level"`
}

// DiscordVoiceConfig configures the Discord voice connector.
type DiscordVoiceConfig struct {
	Enabled        bool   `json:"enabled"`
	Token          string `json:"token"`
	VoiceChannelID string `json:"voice_channel_id"`
	HumanUserID    string `json:"human_user_id"`
}

// DiscordTextConfig configures the Discord text connector.
type DiscordTextConfig struct {
	Enabled     bool   `json:"enabled"`
	Token       string `json:"token"`
	ChannelID   string `json:"channel_id"`
	HumanUserID string `json:"human_user_id"`
}

// MCPExternalConfig configures the persistent external MCP HTTP server.
type MCPExternalConfig struct {
	Enabled       bool     `json:"enabled"`
	ListenAddr    string   `json:"listen_addr"`
	AllowedAgents []string `json:"allowed_agents,omitempty"`
}

// WebUIConfig configures the browser voice-mode listener.
type WebUIConfig struct {
	Enabled    bool   `json:"enabled"`
	ListenAddr string `json:"listen_addr"`
	CertFile   string `json:"cert_file"`
	KeyFile    string `json:"key_file"`
}

// SlackConfig configures the Slack DM connector.
type SlackConfig struct {
	Enabled     bool              `json:"enabled"`
	BotToken    string            `json:"bot_token"`
	AppToken    string            `json:"app_token"`
	Room        string            `json:"room"`
	HumanUserID string            `json:"human_user_id"`
	SocialMode  SlackSocialConfig `json:"social_mode"`
}

// SlackSocialConfig configures mention-triggered Slack channel threads.
type SlackSocialConfig struct {
	Enabled         bool              `json:"enabled"`
	ChannelAgents   map[string]string `json:"channel_agents,omitempty"`
	AllowedUserIDs  []string          `json:"allowed_user_ids,omitempty"`
	ContextMessages int               `json:"context_messages"`
}

// OpenAIConfig configures the OpenAI audio clients.
type OpenAIConfig struct {
	APIKey         string `json:"api_key"`
	APIBaseURL     string `json:"api_base_url"`
	RocketCodeAuth string `json:"rocketcode_auth"`

	STTModel      string `json:"stt_model"`
	STTPrompt     string `json:"stt_prompt"`
	STTAPIKey     string `json:"stt_key"`
	STTAPIBaseURL string `json:"stt_base_url"`

	TTSModel        string `json:"tts_model"`
	TTSVoice        string `json:"tts_voice"`
	TTSInstructions string `json:"tts_instructions"`
	TTSAPIKey       string `json:"tts_key"`
	TTSAPIBaseURL   string `json:"tts_base_url"`
}

// AnthropicConfig configures Anthropic RocketCode clients.
type AnthropicConfig struct {
	APIKey     string `json:"api_key"`
	APIBaseURL string `json:"api_base_url"`
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

	if data, err = migrateThreadAgentsConfig(absPath, data); err != nil {
		return nil, err
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

	if cfg.OpenAI.STTModel == "" {
		cfg.OpenAI.STTModel = "whisper-1"
	}

	if cfg.OpenAI.TTSModel == "" {
		cfg.OpenAI.TTSModel = "tts-1"
	}

	if cfg.OpenAI.TTSVoice == "" {
		cfg.OpenAI.TTSVoice = "alloy"
	}

	if strings.TrimSpace(cfg.Logging.Level) == "" {
		cfg.Logging.Level = "debug"
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func migrateThreadAgentsConfig(path string, data []byte) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return data, nil
	}

	raw, ok := root["thread_agents"]
	if !ok {
		return data, nil
	}

	var old map[string]string
	if err := json.Unmarshal(raw, &old); err != nil {
		return data, nil
	}

	if len(old) == 0 {
		return data, nil
	}

	migrated := make(map[string]ThreadAgent, len(old))
	for prefix, agent := range old {
		migrated[prefix] = ThreadAgent{Agent: agent, PreSeed: false}
	}

	threadAgents, err := json.Marshal(migrated)
	if err != nil {
		return nil, fmt.Errorf("marshal migrated thread_agents: %w", err)
	}

	root["thread_agents"] = threadAgents

	updated, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal migrated config JSON: %w", err)
	}

	updated = append(updated, '\n')

	if err := os.WriteFile(path, updated, 0o600); err != nil {
		return nil, fmt.Errorf("write migrated config: %w", err)
	}

	return updated, nil
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
	if !c.DiscordVoice.Enabled && !c.DiscordText.Enabled && !c.Slack.Enabled && !c.MCPExternal.Enabled && !c.WebUI.Enabled {
		return errors.New("enable at least one connector, web_ui, or mcp_external")
	}

	if c.Slack.Enabled && c.DiscordText.Enabled {
		return errors.New("slack and discord_text are mutually exclusive primary text connectors")
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

	for _, field := range [...]struct {
		value    *string
		fallback string
	}{{&c.OpenAI.STTAPIKey, c.OpenAI.APIKey}, {&c.OpenAI.STTAPIBaseURL, c.OpenAI.APIBaseURL}, {&c.OpenAI.TTSAPIKey, c.OpenAI.APIKey}, {&c.OpenAI.TTSAPIBaseURL, c.OpenAI.APIBaseURL}} {
		if strings.TrimSpace(*field.value) == "" {
			*field.value = field.fallback
		}
	}

	switch strings.TrimSpace(c.OpenAI.RocketCodeAuth) {
	case "", "api_key":
		c.OpenAI.RocketCodeAuth = "api_key"
	case "chatgpt":
	default:
		return errors.New("openai.rocketcode_auth must be api_key or chatgpt")
	}

	if c.OpenAI.RocketCodeAuth == "api_key" && strings.TrimSpace(c.OpenAI.APIKey) == "" {
		return errors.New("openai.api_key is required")
	}

	if err := c.validateMinimumWaitAfterHumanInteraction(); err != nil {
		return err
	}

	if c.DiscordVoice.Enabled {
		for _, field := range [...]struct{ value, message string }{{c.DiscordVoice.Token, "discord_voice.token is required when discord_voice is enabled"}, {c.DiscordVoice.VoiceChannelID, "discord_voice.voice_channel_id is required when discord_voice is enabled"}, {c.DiscordVoice.HumanUserID, "discord_voice.human_user_id is required when discord_voice is enabled"}} {
			if strings.TrimSpace(field.value) == "" {
				return errors.New(field.message)
			}
		}
	}

	if c.DiscordText.Enabled {
		for _, field := range [...]struct{ value, message string }{{c.DiscordText.Token, "discord_text.token is required when discord_text is enabled"}, {c.DiscordText.ChannelID, "discord_text.channel_id is required when discord_text is enabled"}, {c.DiscordText.HumanUserID, "discord_text.human_user_id is required when discord_text is enabled"}} {
			if strings.TrimSpace(field.value) == "" {
				return errors.New(field.message)
			}
		}
	}

	if c.MCPExternal.Enabled && strings.TrimSpace(c.MCPExternal.ListenAddr) == "" {
		return errors.New("mcp_external.listen_addr is required when mcp_external is enabled")
	}

	c.MCPExternal.AllowedAgents = normalizeStringList(c.MCPExternal.AllowedAgents)

	if err := c.validateWebUI(); err != nil {
		return err
	}

	if err := c.validateSlack(); err != nil {
		return err
	}

	return nil
}

func (c *Config) validateWebUI() error {
	c.WebUI.CertFile = strings.TrimSpace(c.WebUI.CertFile)
	c.WebUI.KeyFile = strings.TrimSpace(c.WebUI.KeyFile)

	if (c.WebUI.CertFile == "") != (c.WebUI.KeyFile == "") {
		return errors.New("web_ui.cert_file and web_ui.key_file must be set together")
	}

	if !c.WebUI.Enabled {
		return nil
	}

	listenAddr := strings.TrimSpace(c.WebUI.ListenAddr)
	if listenAddr == "" {
		return errors.New("web_ui.listen_addr is required when web_ui is enabled")
	}

	host, _, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return fmt.Errorf("validate web_ui: parse web_ui.listen_addr: %w", err)
	}

	addr, err := netip.ParseAddr(host)
	if err == nil && addr.Is6() {
		return errors.New("web_ui.listen_addr must be IPv4-only")
	}

	c.WebUI.ListenAddr = listenAddr

	return nil
}

func normalizeStringList(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || slices.Contains(normalized, value) {
			continue
		}

		normalized = append(normalized, value)
	}

	return normalized
}

func (c *Config) validateMinimumWaitAfterHumanInteraction() error {
	minimumWaitRaw := strings.TrimSpace(c.MinimumWaitAfterHumanInteraction)
	if minimumWaitRaw == "" {
		c.MinimumWaitAfterHumanInteractionDuration = 0
		return nil
	}

	minimumWait, err := time.ParseDuration(minimumWaitRaw)
	if err != nil {
		return fmt.Errorf("parse minimum_wait_after_human_interaction: %w", err)
	}

	if minimumWait < 0 {
		return errors.New("minimum_wait_after_human_interaction must be zero or greater")
	}

	c.MinimumWaitAfterHumanInteractionDuration = minimumWait

	return nil
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

	c.Slack.SocialMode.AllowedUserIDs = normalizeStringList(c.Slack.SocialMode.AllowedUserIDs)

	if !c.Slack.SocialMode.Enabled {
		return nil
	}

	if len(c.Slack.SocialMode.AllowedUserIDs) == 0 {
		return errors.New("slack.social_mode.allowed_user_ids is required when slack social mode is enabled")
	}

	if c.Slack.SocialMode.ContextMessages < 0 {
		return errors.New("slack.social_mode.context_messages must be zero or greater")
	}

	if c.Slack.SocialMode.ContextMessages == 0 {
		c.Slack.SocialMode.ContextMessages = 10
	}

	return nil
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
		if token == "" {
			continue
		}

		if _, ok := seen[token]; ok {
			continue
		}

		seen[token] = struct{}{}
		normalized = append(normalized, token)
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
