// Package config loads and validates rocketclaw configuration files.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
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
	Workspace         string                     `json:"workspace"`
	DatabaseURL       string                     `json:"database_url"`
	WorkDir           string                     `json:"-"`
	Overlays          []string                   `json:"overlays,omitempty"`
	Models            map[string]string          `json:"models,omitempty"`
	Environment       []string                   `json:"environment,omitempty"`
	Logging           LoggingConfig              `json:"logging"`
	MCPExternal       MCPExternalConfig          `json:"mcp_external"`
	MCPServers        map[string]MCPServerConfig `json:"mcp_servers,omitempty"`
	Slack             SlackConfig                `json:"slack"`
	OpenAI            OpenAIConfig               `json:"openai"`
	Providers         map[string]OpenAIConfig    `json:"providers,omitempty"`
	AutoApproverModel string                     `json:"auto_approver_model"`
	Instrumentation   InstrumentationConfig      `json:"instrumentation"`
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

// MCPServerConfig configures one outbound MCP server for code mode.
type MCPServerConfig struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// MCP server names follow the same character rules as MCP tool names in the
// official Go SDK (modelcontextprotocol/go-sdk validateToolName): non-empty,
// max 128 runes, and only [A-Za-z0-9_.-].

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
	APIKey                  string `json:"api_key"`
	APIBaseURL              string `json:"api_base_url"`
	RocketCodeAuth          string `json:"rocketcode_auth"`
	AutocompactionThreshold int64  `json:"autocompaction_threshold,omitempty"`
}

// Provider returns the default or named provider configuration.
func (c *Config) Provider(name string) (OpenAIConfig, bool) {
	if name == "openai" {
		return c.OpenAI, true
	}

	provider, ok := c.Providers[name]

	return provider, ok
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
func Load(configPath, secretsARN string, fetcher SecretFetcher) (*Config, error) {
	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	return loadConfigData(absPath, data, secretsARN, fetcher)
}

func loadConfigData(absPath string, data []byte, secretsARN string, fetcher SecretFetcher) (*Config, error) {
	root, err := decodeObject(data, "config")
	if err != nil {
		return nil, err
	}

	if secretsARN != "" {
		body, err := fetcher.SecretString(secretsARN)
		if err != nil {
			return nil, fmt.Errorf("load secret: %w", err)
		}

		secret, err := decodeObject([]byte(body), "secret")
		if err != nil {
			return nil, err
		}

		root = mergeJSON(root, secret).(map[string]any)
	}

	resolved, err := resolveAWS(root, fetcher)
	if err != nil {
		return nil, err
	}

	data, err = json.Marshal(resolved)
	if err != nil {
		return nil, fmt.Errorf("encode resolved config: %w", err)
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
	return loadMCPUsersFile(configPath, "rocketclaw.users.json", "external MCP users file")
}

func loadMCPUsersFile(configPath, filename, label string) (map[string]string, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return nil, errors.New("config path is required")
	}

	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}

	usersPath := filepath.Join(filepath.Dir(absPath), filename)

	info, err := os.Stat(usersPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}

		return nil, fmt.Errorf("stat %s %s: %w", label, usersPath, err)
	}

	if info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("%s %s must have mode 0600", label, usersPath)
	}

	data, err := os.ReadFile(usersPath)
	if err != nil {
		return nil, fmt.Errorf("read %s %s: %w", label, usersPath, err)
	}

	var users map[string]string
	if err := json.Unmarshal(data, &users); err != nil {
		return nil, fmt.Errorf("parse %s %s: %w", label, usersPath, err)
	}

	if users == nil {
		return nil, fmt.Errorf("parse %s %s: must be a JSON object", label, usersPath)
	}

	return users, nil
}

// Validate verifies the configuration is usable.
func (c *Config) Validate() error {
	if c.Workspace == "" {
		return errors.New("workspace is required")
	}

	c.DatabaseURL = strings.TrimSpace(c.DatabaseURL)
	if c.DatabaseURL == "" {
		return errors.New("database_url is required")
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

	if err := normalizeOpenAIConfig("openai", &c.OpenAI); err != nil {
		return err
	}

	for _, name := range slices.Sorted(maps.Keys(c.Providers)) {
		if name == "" || name != strings.TrimSpace(name) || strings.Contains(name, "/") || name == "openai" {
			return fmt.Errorf("providers[%q] name must be non-empty, trimmed, not contain /, and not be openai", name)
		}

		provider := c.Providers[name]
		if err := normalizeOpenAIConfig(fmt.Sprintf("providers[%q]", name), &provider); err != nil {
			return err
		}

		c.Providers[name] = provider
	}

	var err error

	c.AutoApproverModel, err = normalizeOpenAIModel("auto_approver_model", c.AutoApproverModel)
	if err != nil {
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

	if err := c.validateMCPServers(); err != nil {
		return err
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

	provider, apiModel, qualified := strings.Cut(model, "/")
	if !qualified {
		return model, nil
	}

	if provider == "" || apiModel == "" || strings.Contains(apiModel, "/") {
		return "", fmt.Errorf("%s: invalid model %q: expected model or provider/model", field, model)
	}

	if provider == "openai" {
		return apiModel, nil
	}

	return model, nil
}

func normalizeOpenAIConfig(field string, cfg *OpenAIConfig) error {
	switch strings.TrimSpace(cfg.RocketCodeAuth) {
	case "", "api_key":
		cfg.RocketCodeAuth = "api_key"
	case "chatgpt":
		cfg.RocketCodeAuth = "chatgpt"
	default:
		return fmt.Errorf("%s.rocketcode_auth must be api_key or chatgpt", field)
	}

	if cfg.RocketCodeAuth == "api_key" && strings.TrimSpace(cfg.APIKey) == "" {
		return fmt.Errorf("%s.api_key is required when %s.rocketcode_auth is api_key", field, field)
	}

	if cfg.AutocompactionThreshold < 0 {
		return fmt.Errorf("%s.autocompaction_threshold must be a positive integer", field)
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

	seenAt := false

	for _, channel := range c.Slack.Channels {
		if channel.Channel == "@" {
			if seenAt {
				return errors.New("slack.channels may include only one @ entry")
			}

			seenAt = true
		}

		if len(channel.Agents) == 0 {
			return errors.New("slack.channels[].agents is required")
		}

		if len(channel.AllowedUserIDs) == 0 {
			return errors.New("slack.channels[].allowed_user_ids is required")
		}
	}

	return nil
}

func (c *Config) validateMCPServers() error {
	for _, name := range slices.Sorted(maps.Keys(c.MCPServers)) {
		if err := validateMCPServerName(name); err != nil {
			return fmt.Errorf("mcp_servers[%q]: %w", name, err)
		}

		server := c.MCPServers[name]
		server.Command = strings.TrimSpace(server.Command)
		server.URL = strings.TrimSpace(server.URL)
		server.Cwd = strings.TrimSpace(server.Cwd)

		hasCommand := server.Command != ""

		hasURL := server.URL != ""
		switch {
		case hasCommand == hasURL:
			return fmt.Errorf("mcp_servers[%q]: set exactly one of command or url", name)
		case hasURL:
			parsed, err := url.Parse(server.URL)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				return fmt.Errorf("mcp_servers[%q]: url must be an http or https URL", name)
			}
		}

		c.MCPServers[name] = server
	}

	return nil
}

// validateMCPServerName mirrors modelcontextprotocol/go-sdk mcp.validateToolName
// character rules so config keys are no stricter than MCP protocol names.
func validateMCPServerName(name string) error {
	if name == "" {
		return errors.New("name cannot be empty")
	}

	if len(name) > 128 {
		return fmt.Errorf("name exceeds maximum length of 128 characters (current: %d)", len(name))
	}

	var invalid []string

	seen := make(map[rune]bool)

	for _, r := range name {
		if validMCPNameRune(r) {
			continue
		}

		if !seen[r] {
			invalid = append(invalid, fmt.Sprintf("%q", string(r)))
			seen[r] = true
		}
	}

	if len(invalid) > 0 {
		return fmt.Errorf("name contains invalid characters: %s", strings.Join(invalid, ", "))
	}

	return nil
}

func validMCPNameRune(r rune) bool {
	return r >= 'a' && r <= 'z' ||
		r >= 'A' && r <= 'Z' ||
		r >= '0' && r <= '9' ||
		r == '_' || r == '-' || r == '.'
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
	if channel == "" || channel == "@" || strings.HasPrefix(channel, "#") {
		return channel
	}

	return "#" + channel
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
