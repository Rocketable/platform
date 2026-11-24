package config

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws/arn"
)

type Config struct {
	Workspace            string                                      `json:"workspace"`
	DatabaseURL          string                                      `json:"database_url"`
	Attachments          AttachmentsConfig                           `json:"attachments,omitzero"`
	WorkDir              string                                      `json:"-"`
	Revision             string                                      `json:"-"`
	RevisionModified     bool                                        `json:"-"`
	Overlays             []string                                    `json:"overlays,omitempty"`
	Web                  WebConfig                                   `json:"web,omitzero"`
	ServerDisplayName    string                                      `json:"server_display_name,omitempty"`
	Logging              LoggingConfig                               `json:"logging"`
	MCPExternal          MCPExternalConfig                           `json:"mcp_external"`
	MCPServers           map[string]MCPServerConfig                  `json:"mcp_servers,omitempty"`
	OpenCode             map[string]OpenCodeConfig                   `json:"opencode,omitempty"`
	RocketClaws          map[string]RemoteRocketClawConfig           `json:"rocketclaws,omitempty"`
	TrustedRocketClaws   []string                                    `json:"trusted_rocketclaws,omitempty"`
	Slack                SlackConfig                                 `json:"slack"`
	OpenAI               OpenAIConfig                                `json:"openai"`
	Providers            map[string]OpenAIConfig                     `json:"providers,omitempty"`
	AutoApproverModel    string                                      `json:"auto_approver_model"`
	AgentEditorModel     string                                      `json:"agent_editor_model,omitempty"`
	TitleModel           string                                      `json:"title_model,omitempty"`
	Decision             map[DecisionProvider]DecisionProviderConfig `json:"decision,omitempty"`
	AutoApproverDecision AutoApproverDecisionConfig                  `json:"auto_approver_decision,omitzero"`
	PostHog              PostHogConfig                               `json:"posthog,omitzero"`
	Permissions          map[string][]string                         `json:"permissions,omitempty"`
	EmbeddedAgents       EmbeddedAgentsConfig                        `json:"embedded_agents,omitzero"`
}

type Behavior struct {
	AutoApproverModel string `json:"auto_approver_model"`
	Slack             struct {
		Channels []SlackChannelConfig `json:"channels"`
	} `json:"slack"`
	Logging        LoggingConfig        `json:"logging"`
	Overlays       []string             `json:"overlays"`
	EmbeddedAgents EmbeddedAgentsConfig `json:"embedded_agents"`
}

type EmbeddedAgentsConfig struct {
	Main  EmbeddedAgentConfig `json:"main"`
	Voice EmbeddedAgentConfig `json:"voice"`
}

type EmbeddedAgentConfig struct {
	Disabled bool   `json:"disabled"`
	Model    string `json:"model"`
}

func (c *Config) Behavior() Behavior {
	var b Behavior

	b.AutoApproverModel, b.Slack.Channels = c.AutoApproverModel, c.Slack.Channels
	b.Logging, b.Overlays, b.EmbeddedAgents = c.Logging, c.Overlays, c.EmbeddedAgents

	return b
}

func (c *Config) SetBehavior(b *Behavior) {
	c.AutoApproverModel, c.Slack.Channels = b.AutoApproverModel, b.Slack.Channels
	c.Logging, c.Overlays, c.EmbeddedAgents = b.Logging, b.Overlays, b.EmbeddedAgents
}

const DefaultRuntimeDir = ".rocketclaw"

type AttachmentDriver string

const (
	FilesystemAttachments AttachmentDriver = "filesystem"
	S3Attachments         AttachmentDriver = "s3"
)

type AttachmentsConfig struct {
	Driver    AttachmentDriver `json:"driver"`
	Path      string           `json:"path,omitempty"`
	BucketARN string           `json:"bucket_arn,omitempty"`
}

func (c *Config) AttachmentLocation() (AttachmentDriver, string, error) {
	switch c.Attachments.Driver {
	case "", FilesystemAttachments:
		if c.Attachments.BucketARN != "" {
			return "", "", errors.New("filesystem attachments do not accept bucket_arn")
		}

		path := cmp.Or(c.Attachments.Path, filepath.Join(c.RuntimeDirName(), "attachments"))
		if !filepath.IsAbs(path) {
			path = filepath.Join(c.Workspace, path)
		}

		return FilesystemAttachments, path, nil
	case S3Attachments:
		bucket, err := arn.Parse(c.Attachments.BucketARN)
		if err != nil || bucket.Partition == "" || bucket.Service != "s3" || bucket.Region != "" || bucket.AccountID != "" || bucket.Resource == "" || strings.ContainsAny(bucket.Resource, "/:") || c.Attachments.Path != "" {
			return "", "", errors.New("s3 attachments require a bucket_arn (arn:aws:s3:::bucket) and no path")
		}

		return S3Attachments, bucket.Resource, nil
	default:
		return "", "", fmt.Errorf("unknown attachments driver %q", c.Attachments.Driver)
	}
}

func (c *Config) RuntimeDirName() string {
	if strings.TrimSpace(c.WorkDir) != "" {
		return c.WorkDir
	}

	return DefaultRuntimeDir
}

type WebConfig struct {
	Admins []string     `json:"admins,omitempty"`
	Tsnet  *TsnetConfig `json:"tsnet,omitempty"`
}

type TsnetConfig struct {
	Hostname string   `json:"hostname,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	AuthKey  string   `json:"auth_key,omitempty"`
	Origin   string   `json:"-"`
}

type LoggingConfig struct {
	Level string `json:"level"`
}

type MCPExternalConfig struct {
	Enabled    bool              `json:"enabled"`
	ListenAddr string            `json:"listen_addr"`
	Users      map[string]string `json:"users"`
}

type MCPServerConfig struct {
	Command string              `json:"command,omitempty"`
	Args    []string            `json:"args,omitempty"`
	Env     map[string]string   `json:"env,omitempty"`
	Cwd     string              `json:"cwd,omitempty"`
	URL     string              `json:"url,omitempty"`
	Headers map[string]string   `json:"headers,omitempty"`
	Tools   map[string][]string `json:"tools,omitempty"`

	Instructions string `json:"-"`
}

func (c *Config) MCPFacets() map[string]map[string][]string {
	facets := make(map[string]map[string][]string, len(c.MCPServers))
	for name := range c.MCPServers {
		facets[name] = c.MCPServers[name].Tools
	}

	return facets
}

func (c *Config) ProviderNames() []string {
	return append(slices.Sorted(maps.Keys(c.Providers)), "openai")
}

type OpenCodeConfig struct {
	URL      string `json:"url"`
	Password string `json:"password"`
}

type RemoteRocketClawConfig struct {
	URL string `json:"url"`
}

type SlackConfig struct {
	BotToken string               `json:"bot_token"`
	AppToken string               `json:"app_token"`
	Channels []SlackChannelConfig `json:"channels,omitempty"`
}

func (s SlackConfig) Channel(name string) (SlackChannelConfig, bool) {
	for _, channel := range s.Channels {
		if channel.Channel == name {
			return channel, true
		}
	}

	return SlackChannelConfig{}, false
}

type SlackChannelConfig struct {
	Channel        string   `json:"channel"`
	Agents         []string `json:"agents,omitempty"`
	AllowedUserIDs []string `json:"allowed_user_ids,omitempty"`
}

type OpenAIConfig struct {
	APIKey                             string `json:"api_key"`
	APIBaseURL                         string `json:"api_base_url"`
	Auth                               string `json:"auth"`
	AutocompactionThreshold            int64  `json:"autocompaction_threshold,omitempty"`
	PromptCacheMarkersRelativePosition *int   `json:"prompt_cache_markers_relative_position,omitempty"`
}

func (c *OpenAIConfig) AuthMode() string {
	return cmp.Or(strings.TrimSpace(c.Auth), "api_key")
}

type DecisionProvider string

const (
	DecisionOpenAI   DecisionProvider = "openai"
	DecisionTypeSafe DecisionProvider = "typesafe"
)

func (c *Config) DecisionProviderNames() []string {
	names := make([]string, 0, len(c.Decision))
	for name := range c.Decision {
		names = append(names, string(name))
	}

	return names
}

type DecisionProviderConfig struct {
	APIKey     string `json:"api_key"`
	APIBaseURL string `json:"api_base_url,omitempty"`
}

type AutoApproverDecisionConfig struct {
	Model         string  `json:"model,omitempty"`
	MinConfidence float64 `json:"min_confidence,omitempty"`
}

func (c *Config) Provider(name string) (OpenAIConfig, bool) {
	if name == "openai" {
		return c.OpenAI, true
	}

	provider, ok := c.Providers[name]

	return provider, ok
}

type PostHogConfig struct {
	ProjectToken string `json:"project_token,omitempty"`
	Host         string `json:"host,omitempty"`
}

func Load(configPath, secretsARN string, fetcher SecretFetcher) (*Config, error) {
	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}

	data, err := os.ReadFile(absPath)
	if errors.Is(err, os.ErrNotExist) && secretsARN != "" {
		data, err = []byte("{}"), nil
	}

	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	root, err := decodeObject(data, "config")
	if err != nil {
		return nil, err
	}

	if secretsARN != "" {
		body, err := fetcher.SecretString(context.Background(), secretsARN)
		if err != nil {
			return nil, fmt.Errorf("load secret: %w", err)
		}

		secret, err := decodeObject([]byte(body), "secret")
		if err != nil {
			return nil, err
		}

		mergeJSON(root, secret)
	}

	resolved, err := resolveAWS(context.Background(), root, fetcher)
	if err != nil {
		return nil, err
	}

	data, _ = json.Marshal(resolved)

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config JSON: %w", err)
	}

	configDir := filepath.Dir(absPath)
	cfg.Workspace = cmp.Or(cfg.Workspace, configDir)

	if !filepath.IsAbs(cfg.Workspace) {
		cfg.Workspace = filepath.Join(configDir, cfg.Workspace)
	}

	cfg.Workspace = filepath.Clean(cfg.Workspace)

	return &cfg, nil
}

func (c *Config) ValidateTsnet() error {
	tsnet := c.Web.Tsnet
	if tsnet == nil {
		return errors.New("web.tsnet is required: the Web is served only on the tailnet")
	}

	tsnet.Hostname = cmp.Or(tsnet.Hostname, "rocketclaw")
	if strings.Trim(tsnet.Hostname, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" || slices.ContainsFunc(tsnet.Tags, func(tag string) bool { return !strings.HasPrefix(tag, "tag:") }) {
		return errors.New(`web.tsnet.hostname must use only a-z, 0-9 and -, and each web.tsnet.tags entry must start with "tag:"`)
	}

	return nil
}

func (c *Config) Validate() error {
	if _, _, err := c.AttachmentLocation(); err != nil {
		return err
	}

	if err := c.ValidateTsnet(); err != nil {
		return err
	}

	if c.Workspace == "" {
		return errors.New("workspace is required")
	}

	c.DatabaseURL = strings.TrimSpace(c.DatabaseURL)
	if c.DatabaseURL == "" {
		return errors.New("database_url is required")
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

	if err := c.validateDecision(); err != nil {
		return err
	}

	c.PostHog.Host = strings.TrimRight(cmp.Or(c.PostHog.Host, "https://us.i.posthog.com"), "/")
	if c.PostHog.ProjectToken != "" && !strings.HasPrefix(c.PostHog.ProjectToken, "phc_") {
		return errors.New("posthog.project_token must be a public project token (phc_...), never a personal API key")
	}

	if c.MCPExternal.Enabled && strings.TrimSpace(c.MCPExternal.ListenAddr) == "" {
		return errors.New("mcp_external.listen_addr is required when mcp_external is enabled")
	}

	if err := c.validateMCPServers(); err != nil {
		return err
	}

	if err := c.validateOpenCode(); err != nil {
		return err
	}

	if err := c.validateRocketClaws(); err != nil {
		return err
	}

	if err := c.validatePermissions(); err != nil {
		return err
	}

	if c.Slack.BotToken == "" && c.Slack.AppToken == "" {
		return nil
	}

	for _, field := range [...]struct{ value, message string }{{c.Slack.BotToken, "slack.bot_token is required"}, {c.Slack.AppToken, "slack.app_token is required"}} {
		if strings.TrimSpace(field.value) == "" {
			return errors.New(field.message)
		}
	}

	return nil
}

func (c *Config) ValidateBehavior() error {
	if strings.TrimSpace(c.Logging.Level) == "" {
		c.Logging.Level = "debug"
	}

	c.Overlays = normalizeStrings(c.Overlays)

	var err error

	c.AutoApproverModel, err = normalizeOpenAIModel("auto_approver_model", c.AutoApproverModel)
	if err != nil {
		return err
	}

	for _, agent := range [...]struct {
		field   string
		setting *EmbeddedAgentConfig
	}{{"embedded_agents.main.model", &c.EmbeddedAgents.Main}, {"embedded_agents.voice.model", &c.EmbeddedAgents.Voice}} {
		if agent.setting.Model, err = normalizeOpenAIModel(agent.field, agent.setting.Model); err != nil {
			return err
		}
	}

	return c.validateSlack()
}

func (c *Config) PermittedSpaces(login string) []string {
	for listed, spaces := range c.Permissions {
		if strings.EqualFold(listed, login) {
			return spaces
		}
	}

	return nil
}

func normalizeOpenAIModel(field, model string) (string, error) {
	model = strings.TrimSpace(model)

	provider, apiModel, qualified := strings.Cut(model, "/")
	if !qualified {
		return model, nil
	}

	if provider == "" || apiModel == "" || strings.Contains(model, "//") || strings.HasSuffix(apiModel, "/") {
		return "", fmt.Errorf("%s: invalid model %q: expected model or provider/model", field, model)
	}

	if provider == "openai" {
		return apiModel, nil
	}

	return model, nil
}

func normalizeOpenAIConfig(field string, cfg *OpenAIConfig) error {
	switch mode := cfg.AuthMode(); mode {
	case "api_key", "chatgpt":
		cfg.Auth = mode
	default:
		return fmt.Errorf("%s.auth must be api_key or chatgpt", field)
	}

	if cfg.Auth == "api_key" && strings.TrimSpace(cfg.APIKey) == "" {
		return fmt.Errorf("%s.api_key is required when %s.auth is api_key", field, field)
	}

	if cfg.AutocompactionThreshold < 0 {
		return fmt.Errorf("%s.autocompaction_threshold must be a positive integer", field)
	}

	if position := cfg.PromptCacheMarkersRelativePosition; position != nil && *position >= 0 {
		return fmt.Errorf("%s.prompt_cache_markers_relative_position must be a negative integer", field)
	}

	return nil
}

func normalizeStringList(values []string) []string {
	unique := []string{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" && !slices.Contains(unique, value) {
			unique = append(unique, value)
		}
	}

	return unique
}

func (c *Config) validateSlack() error {
	normalized := make([]SlackChannelConfig, 0, len(c.Slack.Channels))
	for _, channel := range c.Slack.Channels {
		channel.Channel = strings.TrimSpace(channel.Channel)
		if channel.Channel != "" && channel.Channel != "@" && !strings.HasPrefix(channel.Channel, "#") {
			channel.Channel = "#" + channel.Channel
		}

		channel.Agents = normalizeStringList(channel.Agents)

		channel.AllowedUserIDs = normalizeStringList(channel.AllowedUserIDs)
		if channel.Channel != "" {
			normalized = append(normalized, channel)
		}
	}

	c.Slack.Channels = normalized
	if len(c.Slack.Channels) == 0 && c.Slack.BotToken != "" {
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

		if strings.Contains(name, ".") {
			return fmt.Errorf(`mcp_servers[%q]: name must not contain "."`, name)
		}

		server := c.MCPServers[name]
		server.Command = strings.TrimSpace(server.Command)
		server.URL = strings.TrimSpace(server.URL)
		server.Cwd = strings.TrimSpace(server.Cwd)

		switch {
		case (server.Command != "") == (server.URL != ""):
			return fmt.Errorf("mcp_servers[%q]: set exactly one of command or url", name)
		case server.URL != "":
			parsed, err := url.Parse(server.URL)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				return fmt.Errorf("mcp_servers[%q]: url must be an http or https URL", name)
			}
		}

		for _, facet := range slices.Sorted(maps.Keys(server.Tools)) {
			if facet == "" || strings.Trim(facet, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" {
				return fmt.Errorf(`mcp_servers[%q]: tools[%q]: facet names use only letters, digits, "_" and "-"`, name, facet)
			}

			for _, pattern := range server.Tools[facet] {
				if tool := strings.TrimPrefix(pattern, "-"); tool == "" || strings.Trim(tool, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-./*?") != "" {
					return fmt.Errorf(`mcp_servers[%q]: tools[%q]: pattern %q may use only tool-name characters, "*" and "?"`, name, facet, pattern)
				}
			}
		}

		c.MCPServers[name] = server
	}

	return nil
}

func (c *Config) validateDecision() error {
	for _, name := range slices.Sorted(maps.Keys(c.Decision)) {
		switch {
		case name != DecisionOpenAI && name != DecisionTypeSafe:
			return fmt.Errorf("decision[%q]: provider must be openai or typesafe", name)
		case strings.TrimSpace(c.Decision[name].APIKey) == "":
			return fmt.Errorf("decision[%q].api_key is required", name)
		}
	}

	decision := &c.AutoApproverDecision

	decision.Model = strings.TrimSpace(decision.Model)
	if provider, model, _ := strings.Cut(decision.Model, "/"); decision.Model != "" && (model == "" || c.Decision[DecisionProvider(provider)] == DecisionProviderConfig{}) {
		return fmt.Errorf("auto_approver_decision.model: %q must be provider/model with its provider configured in decision", decision.Model)
	}

	if decision.MinConfidence < 0 || decision.MinConfidence > 1 {
		return errors.New("auto_approver_decision.min_confidence must be between 0 and 1")
	}

	return nil
}

func (c *Config) validateRocketClaws() error {
	for _, name := range slices.Sorted(maps.Keys(c.RocketClaws)) {
		if name == "" || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" || !httpsOrigin(c.RocketClaws[name].URL) {
			return fmt.Errorf("rocketclaws[%q]: name must use a-z, 0-9 and -, and url must be an https origin with no path", name)
		}
	}

	for i, origin := range c.TrustedRocketClaws {
		if !httpsOrigin(origin) {
			return fmt.Errorf("trusted_rocketclaws[%d]: must be an https origin with no path", i)
		}
	}

	return nil
}

func (c *Config) validatePermissions() error {
	logins := slices.Sorted(maps.Keys(c.Permissions))
	for i, login := range logins {
		spaces := c.Permissions[login]

		switch {
		case login == "" || strings.TrimSpace(login) != login:
			return fmt.Errorf("permissions[%q]: login must not be blank or start or end with spaces", login)
		case slices.ContainsFunc(logins[:i], func(other string) bool { return strings.EqualFold(other, login) }):
			return fmt.Errorf("permissions[%q]: another entry has that login, ignoring case", login)
		case len(spaces) == 0:
			return fmt.Errorf("permissions[%q]: list at least one space", login)
		case slices.ContainsFunc(spaces, func(space string) bool { return space == "" || strings.TrimSpace(space) != space }):
			return fmt.Errorf("permissions[%q]: space names must not be blank or start or end with spaces", login)
		}
	}

	return nil
}

func httpsOrigin(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && !strings.ContainsAny(raw, "?#") && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}

func (c *Config) validateOpenCode() error {
	for _, name := range slices.Sorted(maps.Keys(c.OpenCode)) {
		server := c.OpenCode[name]
		errName := validateMCPServerName(name)
		parsed, errURL := url.Parse(server.URL)

		switch {
		case errName != nil:
			return fmt.Errorf("opencode[%q]: %w", name, errName)
		case errURL != nil || (parsed.Scheme != "http" && parsed.Scheme != "https"):
			return fmt.Errorf("opencode[%q]: url must be an http or https URL", name)
		case server.Password == "":
			return fmt.Errorf("opencode[%q]: password is required", name)
		}
	}

	return nil
}

func validateMCPServerName(name string) error {
	if name == "" {
		return errors.New("name cannot be empty")
	}

	if len(name) > 128 {
		return fmt.Errorf("name exceeds maximum length of 128 characters (current: %d)", len(name))
	}

	var invalid []string

	for _, r := range name {
		if r >= 'a' && r <= 'z' ||
			r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' ||
			r == '_' || r == '-' || r == '.' {
			continue
		}

		if quoted := fmt.Sprintf("%q", string(r)); !slices.Contains(invalid, quoted) {
			invalid = append(invalid, quoted)
		}
	}

	if len(invalid) > 0 {
		return fmt.Errorf("name contains invalid characters: %s", strings.Join(invalid, ", "))
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
