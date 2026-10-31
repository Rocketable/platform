// Package config loads and validates rocketclaw configuration files.
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

// Config is the top-level rocketclaw runtime configuration.
type Config struct {
	Workspace         string                            `json:"workspace"`
	DatabaseURL       string                            `json:"database_url"`
	Attachments       AttachmentsConfig                 `json:"attachments,omitzero"`
	WorkDir           string                            `json:"-"`
	Overlays          []string                          `json:"overlays,omitempty"`
	Web               WebConfig                         `json:"web,omitzero"`
	ServerDisplayName string                            `json:"server_display_name,omitempty"`
	Environment       []string                          `json:"environment,omitempty"`
	Logging           LoggingConfig                     `json:"logging"`
	MCPExternal       MCPExternalConfig                 `json:"mcp_external"`
	MCPServers        map[string]MCPServerConfig        `json:"mcp_servers,omitempty"`
	OpenCode          map[string]OpenCodeConfig         `json:"opencode,omitempty"`
	RocketClaws       map[string]RemoteRocketClawConfig `json:"rocketclaws,omitempty"`
	// TrustedRocketClaws are the https origins of the RocketClaw pages whose browsers may call this server's tailnet
	// address as the person on the connection; a page from any other origin gets no cross-site access.
	TrustedRocketClaws []string                `json:"trusted_rocketclaws,omitempty"`
	Slack              SlackConfig             `json:"slack"`
	OpenAI             OpenAIConfig            `json:"openai"`
	Providers          map[string]OpenAIConfig `json:"providers,omitempty"`
	AutoApproverModel  string                  `json:"auto_approver_model"`
	Instrumentation    InstrumentationConfig   `json:"instrumentation"`
	PostHog            PostHogConfig           `json:"posthog,omitzero"`
}

// DefaultRuntimeDir is the generated runtime directory for rocketclaw configs.
const DefaultRuntimeDir = ".rocketclaw"

// AttachmentDriver selects where immutable attachment originals are stored.
type AttachmentDriver string

// FilesystemAttachments and S3Attachments select the supported original-file stores.
const (
	FilesystemAttachments AttachmentDriver = "filesystem"
	S3Attachments         AttachmentDriver = "s3"
)

// AttachmentsConfig selects an attachment directory or S3 bucket.
type AttachmentsConfig struct {
	Driver    AttachmentDriver `json:"driver"`
	Path      string           `json:"path,omitempty"`
	BucketARN string           `json:"bucket_arn,omitempty"`
}

// AttachmentLocation validates storage configuration and resolves its directory or bucket name.
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

// RuntimeDirName returns the selected generated runtime directory name.
func (c *Config) RuntimeDirName() string {
	if strings.TrimSpace(c.WorkDir) != "" {
		return c.WorkDir
	}

	return DefaultRuntimeDir
}

// WebConfig controls the Web listener and conversation presentation.
type WebConfig struct {
	ListenAddress string `json:"listen_address,omitempty"`
	// TLSCertFile and TLSKeyFile are the PEM certificate and key the listen_address listener serves
	// HTTPS with. Without them it serves a self-signed certificate made at each start.
	TLSCertFile string       `json:"tls_cert_file,omitempty"`
	TLSKeyFile  string       `json:"tls_key_file,omitempty"`
	Sentry      SentryConfig `json:"sentry,omitzero"`
	Tsnet       *TsnetConfig `json:"tsnet,omitempty"`
}

// TsnetConfig runs an embedded Tailscale node serving the Web over tailnet HTTPS.
// Its auth key comes only from TS_AUTHKEY, never from config.
type TsnetConfig struct {
	Hostname string   `json:"hostname,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	// AuthKey logs the node in without a login link; it is used only while the node is logged out.
	AuthKey string `json:"auth_key,omitempty"`
	// Origin is the node's HTTPS origin, set at runtime once it serves.
	Origin string `json:"-"`
}

// SentryConfig contains only public browser tracing settings, never an auth token.
type SentryConfig struct {
	DSN              string   `json:"dsn,omitempty"`
	Environment      string   `json:"environment,omitempty"`
	TracesSampleRate *float64 `json:"traces_sample_rate,omitempty"`
}

func (c *WebConfig) validate() error {
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return errors.New("web.tls_cert_file and web.tls_key_file must be set together")
	}

	return c.Sentry.validate()
}

func (c SentryConfig) validate() error {
	if rate := c.TracesSampleRate; rate != nil && (*rate < 0 || *rate > 1) {
		return errors.New("web.sentry.traces_sample_rate must be between 0 and 1")
	}

	if c.DSN != "" {
		parsed, err := url.Parse(c.DSN)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Hostname() == "" || parsed.User == nil || parsed.User.Username() == "" || parsed.Path == "" || parsed.Path == "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return errors.New("web.sentry.dsn must be a public Sentry DSN (https://public-key@host/project-id)")
		}

		if _, private := parsed.User.Password(); private {
			return errors.New("web.sentry.dsn must not contain a private key")
		}
	}

	return nil
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
	Tools   MCPToolGroups     `json:"tools,omitzero"`
}

// MCPToolGroups lists one server's read and write tool-name patterns, the groups easy agents select.
type MCPToolGroups struct {
	Read  []string `json:"read,omitempty"`
	Write []string `json:"write,omitempty"`
}

// MCPToolGroups returns every MCP server's tool groups keyed by group name.
func (c *Config) MCPToolGroups() map[string]map[string][]string {
	groups := make(map[string]map[string][]string, len(c.MCPServers))
	for name := range c.MCPServers {
		groups[name] = map[string][]string{"read": c.MCPServers[name].Tools.Read, "write": c.MCPServers[name].Tools.Write}
	}

	return groups
}

// ProviderNames returns every configured model provider name, openai included.
func (c *Config) ProviderNames() []string {
	return append(slices.Sorted(maps.Keys(c.Providers)), "openai")
}

// OpenCodeConfig names one remote OpenCode server the Web lists and proxies under /oc/{name}/.
type OpenCodeConfig struct {
	URL      string `json:"url"`
	Password string `json:"password"`
}

// RemoteRocketClawConfig names another RocketClaw whose chats the Web page lists and operates.
// URL is the remote's tsnet HTTPS origin, which the browser calls directly.
type RemoteRocketClawConfig struct {
	URL string `json:"url"`
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

// Channel returns the configured Slack channel with this name.
func (s SlackConfig) Channel(name string) (SlackChannelConfig, bool) {
	for _, channel := range s.Channels {
		if channel.Channel == name {
			return channel, true
		}
	}

	return SlackChannelConfig{}, false
}

// SlackChannelConfig configures one Slack channel.
type SlackChannelConfig struct {
	Channel        string   `json:"channel"`
	Agents         []string `json:"agents,omitempty"`
	AllowedUserIDs []string `json:"allowed_user_ids,omitempty"`
}

// OpenAIConfig configures the OpenAI client used by the agent harness.
type OpenAIConfig struct {
	APIKey     string `json:"api_key"`
	APIBaseURL string `json:"api_base_url"`
	Auth       string `json:"auth"`
	// LegacyAuth is the old name of Auth, read when Auth is unset.
	LegacyAuth              string `json:"rocketcode_auth,omitempty"`
	AutocompactionThreshold int64  `json:"autocompaction_threshold,omitempty"`
	// PromptCacheMarkersRelativePosition, when set, marks the text piece at this
	// negative index of the last request message with an Anthropic cache_control.
	PromptCacheMarkersRelativePosition *int `json:"prompt_cache_markers_relative_position,omitempty"`
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

// PostHogConfig sends log events, errors, and browser sessions to PostHog Cloud when ProjectToken is set,
// and traces too while instrumentation is enabled. The project token is public: the browser receives it.
type PostHogConfig struct {
	ProjectToken string `json:"project_token,omitempty"`
	Host         string `json:"host,omitempty"`
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

		root = mergeJSON(root, secret).(map[string]any)
	}

	resolved, err := resolveAWS(context.Background(), root, fetcher)
	if err != nil {
		return nil, err
	}

	// Resolution only replaces decoded JSON values with fetched strings.
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
	c.Web.ListenAddress = cmp.Or(c.Web.ListenAddress, "0.0.0.0:3000")
	if _, _, err := c.AttachmentLocation(); err != nil {
		return err
	}

	if err := c.Web.validate(); err != nil {
		return err
	}

	if tsnet := c.Web.Tsnet; tsnet != nil {
		// The hostname names the state directory, so it must stay a DNS label.
		tsnet.Hostname = cmp.Or(tsnet.Hostname, "rocketclaw")
		if strings.Trim(tsnet.Hostname, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" || slices.ContainsFunc(tsnet.Tags, func(tag string) bool { return !strings.HasPrefix(tag, "tag:") }) {
			return errors.New(`web.tsnet.hostname must use only a-z, 0-9 and -, and each web.tsnet.tags entry must start with "tag:"`)
		}
	}

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

	if err := c.validateInstrumentation(); err != nil {
		return err
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

	return c.validateSlack()
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
	switch strings.TrimSpace(cmp.Or(cfg.Auth, cfg.LegacyAuth)) {
	case "", "api_key":
		cfg.Auth = "api_key"
	case "chatgpt":
		cfg.Auth = "chatgpt"
	default:
		return fmt.Errorf("%s.auth must be api_key or chatgpt", field)
	}

	cfg.LegacyAuth = ""

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

		switch {
		case (server.Command != "") == (server.URL != ""):
			return fmt.Errorf("mcp_servers[%q]: set exactly one of command or url", name)
		case server.URL != "":
			parsed, err := url.Parse(server.URL)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				return fmt.Errorf("mcp_servers[%q]: url must be an http or https URL", name)
			}
		}

		for _, read := range server.Tools.Read {
			for _, write := range server.Tools.Write {
				// A shared pattern, or a pattern matching a literal name in the other group, puts a tool in both
				// groups. filepath.Match agrees with permission wildcards on MCP tool names ([A-Za-z0-9_.-]).
				readWrite, _ := filepath.Match(read, write)
				writeRead, _ := filepath.Match(write, read)

				if readWrite || writeRead {
					return fmt.Errorf("mcp_servers[%q]: tools.read %q and tools.write %q match the same tool", name, read, write)
				}
			}
		}

		c.MCPServers[name] = server
	}

	return nil
}

func (c *Config) validateInstrumentation() error {
	c.Instrumentation.CollectorEndpoint = strings.TrimSpace(c.Instrumentation.CollectorEndpoint)
	c.Instrumentation.ProjectName = strings.TrimSpace(c.Instrumentation.ProjectName)

	c.Instrumentation.APIKey = strings.TrimSpace(c.Instrumentation.APIKey)
	if c.Instrumentation.Enabled && c.Instrumentation.CollectorEndpoint == "" && c.PostHog.ProjectToken == "" {
		return errors.New("instrumentation.collector_endpoint or posthog.project_token is required when instrumentation is enabled")
	}

	c.PostHog.Host = strings.TrimRight(cmp.Or(c.PostHog.Host, "https://us.i.posthog.com"), "/")
	if c.PostHog.ProjectToken != "" && !strings.HasPrefix(c.PostHog.ProjectToken, "phc_") {
		return errors.New("posthog.project_token must be a public project token (phc_...), never a personal API key")
	}

	return nil
}

func (c *Config) validateRocketClaws() error {
	for _, name := range slices.Sorted(maps.Keys(c.RocketClaws)) {
		// Names appear in page URLs as /r/{name}/; the URL is a bare origin the browser calls.
		if name == "" || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" || !httpsOrigin(c.RocketClaws[name].URL) {
			return fmt.Errorf("rocketclaws[%q]: name must use a-z, 0-9 and -, and url must be an https origin with no path", name)
		}
	}

	// A trusted page is matched against the Origin its browser sends, which is a bare origin too.
	for i, origin := range c.TrustedRocketClaws {
		if !httpsOrigin(origin) {
			return fmt.Errorf("trusted_rocketclaws[%d]: must be an https origin with no path", i)
		}
	}

	return nil
}

// httpsOrigin reports whether raw is an https origin with nothing after its host and port.
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
