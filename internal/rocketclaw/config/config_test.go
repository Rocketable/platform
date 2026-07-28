package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExampleConfigUsesDirectSlackChannels(t *testing.T) {
	data, err := os.ReadFile("../rocketclaw.example.json")
	require.NoError(t, err)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raw))

	for _, removed := range []string{"thread_agents", "pre_seed", "context_messages", "seed_compaction_model"} {
		require.NotContains(t, string(data), `"`+removed+`"`)
	}

	var cfg Config
	require.NoError(t, json.Unmarshal(data, &cfg))
	require.NoError(t, cfg.Validate())

	var slack map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw["slack"], &slack))
	require.NotContains(t, slack, "enabled")
	require.NotEmpty(t, cfg.Slack.Channels)
	assert.NotEmpty(t, cfg.Slack.Channels[0].Agents)
	assert.NotEmpty(t, cfg.Slack.Channels[0].AllowedUserIDs)
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "openai": {"api_key": "test-key"},
	  "slack": {
	    "bot_token": "xoxb-test",
	    "app_token": "xapp-test",
	    "channels": [{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]
	  }
	}`)

	assert.Equal(t, "api_key", cfg.OpenAI.RocketCodeAuth)
	assert.Empty(t, cfg.AutoApproverModel)
	assert.True(t, filepath.IsAbs(cfg.Workspace))
}

func TestLoadPreservesNamedProviders(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "models": {"coding-high": "work/software-development-sol", "review-fast": "gpt-5.6-luna"},
	  "auto_approver_model": " openai/gpt-5.5 ",
	  "openai": {"api_key": "test-key"},
	  "providers": {
	    "work": {"api_base_url": " https://work.example/v1/ ", "rocketcode_auth": "chatgpt"}
	  },
	  "slack": {"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},
	  "mcp_external": {"enabled": true, "listen_addr": "127.0.0.1:8765"}
	}`)

	assert.Equal(t, "openai/gpt-5.5", cfg.AutoApproverModel)
	assert.Equal(t, map[string]string{"coding-high": "work/software-development-sol", "review-fast": "gpt-5.6-luna"}, cfg.Models)
	provider, ok := cfg.Provider("work")
	require.True(t, ok)
	assert.Equal(t, OpenAIConfig{APIBaseURL: "https://work.example/v1", RocketCodeAuth: "chatgpt"}, provider)
	provider, ok = cfg.Provider("openai")
	require.True(t, ok)
	assert.Equal(t, cfg.OpenAI, provider)
}

func TestValidateRejectsInvalidProviderNames(t *testing.T) {
	for _, name := range []string{"", " work", "work ", "openai", ".", "../work", "team/work"} {
		t.Run(name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Providers = map[string]OpenAIConfig{name: {APIKey: "key"}}

			err := cfg.Validate()
			require.ErrorContains(t, err, "provider")
		})
	}
}

func TestValidateRejectsUnknownOrMalformedModelReferences(t *testing.T) {
	for _, tt := range []struct {
		name, wantErr string
		update        func(*Config)
	}{
		{name: "empty alias", update: func(c *Config) { c.Models = map[string]string{" ": "gpt-5.5"} }, wantErr: "models"},
		{name: "empty alias model", update: func(c *Config) { c.Models = map[string]string{"coding-high": " "} }, wantErr: "models"},
		{name: "unknown alias provider", update: func(c *Config) { c.Models = map[string]string{"coding-high": "missing/gpt-5.5"} }, wantErr: `models["coding-high"]`},
		{name: "empty alias qualified model", update: func(c *Config) { c.Models = map[string]string{"coding-high": "work/"} }, wantErr: `models["coding-high"]`},
		{name: "missing alias provider", update: func(c *Config) { c.Models = map[string]string{"coding-high": "/gpt-5.5"} }, wantErr: `models["coding-high"]`},
		{name: "unknown auto approver provider", update: func(c *Config) { c.AutoApproverModel = "missing/gpt-5.5" }, wantErr: "auto_approver_model"},
		{name: "empty auto approver qualified model", update: func(c *Config) { c.AutoApproverModel = "work/" }, wantErr: "auto_approver_model"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Providers = map[string]OpenAIConfig{"work": {APIKey: "key"}}
			tt.update(cfg)

			err := cfg.Validate()
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestValidateNormalizesProviderBaseURLs(t *testing.T) {
	cfg := validConfig()
	cfg.OpenAI.APIBaseURL = " https://api.openai.com/v1/// "
	cfg.Providers = map[string]OpenAIConfig{
		"work":  {APIKey: " work-key ", APIBaseURL: " https://work.example/v1/// "},
		"oauth": {APIBaseURL: " ", RocketCodeAuth: " chatgpt "},
	}

	require.NoError(t, cfg.Validate())
	assert.Equal(t, "https://api.openai.com/v1", cfg.OpenAI.APIBaseURL)
	assert.Equal(t, "https://work.example/v1", cfg.Providers["work"].APIBaseURL)
	assert.Empty(t, cfg.Providers["oauth"].APIBaseURL)
	assert.Equal(t, "chatgpt", cfg.Providers["oauth"].RocketCodeAuth)
}

func TestValidateRejectsUnsafeProviderBaseURLs(t *testing.T) {
	for _, rawURL := range []string{
		"api.example.test/v1",
		"/v1",
		"ftp://api.example.test/v1",
		"https://api.example.test/v1?token=secret",
		"https://api.example.test/v1#private",
		"https://user:password@api.example.test/v1",
		"https:///v1",
	} {
		t.Run(rawURL, func(t *testing.T) {
			cfg := validConfig()
			cfg.OpenAI.APIBaseURL = rawURL

			err := cfg.Validate()
			require.ErrorContains(t, err, "openai.api_base_url")
		})
	}
}

func TestValidateAcceptsAbsoluteHTTPProviderBaseURLs(t *testing.T) {
	for _, rawURL := range []string{"http://localhost:8080/v1/", "https://api.example.test/openresponses///", " "} {
		t.Run(rawURL, func(t *testing.T) {
			cfg := validConfig()
			cfg.OpenAI.APIBaseURL = rawURL

			require.NoError(t, cfg.Validate())
			require.Equal(t, strings.TrimRight(strings.TrimSpace(rawURL), "/"), cfg.OpenAI.APIBaseURL)
		})
	}
}

func TestRenderAgentModelResolvesNamedProviderAlias(t *testing.T) {
	cfg := validConfig()
	cfg.Providers = map[string]OpenAIConfig{"work": {APIKey: "key"}}
	cfg.Models = map[string]string{
		"coding-high":        "work/software-development-sol",
		`quoted"placeholder`: "gpt-5.6-luna",
		"nested":             `{{ model "coding-high" }}`,
	}
	require.NoError(t, cfg.Validate())

	for _, tt := range []struct {
		name    string
		model   string
		want    string
		wantErr string
	}{
		{name: "concrete", model: "gpt-5.5", want: "gpt-5.5"},
		{name: "explicit openai", model: "openai/gpt-5.5", want: "openai/gpt-5.5"},
		{name: "placeholder", model: `{{ model "coding-high" }}`, want: "work/software-development-sol"},
		{name: "arbitrary quoted name", model: `{{ model "quoted\"placeholder" }}`, want: "gpt-5.6-luna"},
		{name: "missing", model: `{{ model "missing" }}`, wantErr: `model "missing" is not configured`},
		{name: "invalid template", model: `{{ model "coding-high" }`, wantErr: "parse model template"},
		{name: "empty result", model: `{{ "" }}`, wantErr: "model template returned an empty model"},
		{name: "nested", model: `{{ model "nested" }}`, wantErr: "model template returned another template"},
		{name: "unknown provider", model: "missing/gpt-5.5", wantErr: "missing/gpt-5.5"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cfg.RenderAgentModel(tt.model)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestLoadPreservesInstrumentationConfig(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "openai": {"api_key": "test-key"},
	  "slack": {"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},
	  "instrumentation": {
	    "enabled": true,
	    "collector_endpoint": "http://localhost:6006",
	    "project_name": "rocketclaw-dev",
	    "api_key": "phoenix-key",
	    "hide_inputs": true,
	    "hide_outputs": true
	  },
	  "mcp_external": {"enabled": true, "listen_addr": "127.0.0.1:8765"}
	}`)

	assert.True(t, cfg.Instrumentation.Enabled)
	assert.Equal(t, "http://localhost:6006", cfg.Instrumentation.CollectorEndpoint)
	assert.Equal(t, "rocketclaw-dev", cfg.Instrumentation.ProjectName)
	assert.Equal(t, "phoenix-key", cfg.Instrumentation.APIKey)
	assert.True(t, cfg.Instrumentation.HideInputs)
	assert.True(t, cfg.Instrumentation.HideOutputs)
}

func TestValidateRejectsEnabledInstrumentationWithoutEndpoint(t *testing.T) {
	cfg := validConfig()
	cfg.Instrumentation.Enabled = true

	err := cfg.Validate()

	require.ErrorContains(t, err, "instrumentation.collector_endpoint is required")
}

func TestLoadNormalizesOverlays(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "overlays": [" github.com/rocketable/overlay1@main ", "", "github.com/rocketable/overlay2"],
	  "openai": {"api_key": "test-key"},
	  "slack": {"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},
	  "mcp_external": {"enabled": true, "listen_addr": "127.0.0.1:8765"}
	}`)

	assert.Equal(t, []string{"github.com/rocketable/overlay1@main", "github.com/rocketable/overlay2"}, cfg.Overlays)
}

func TestLoadDefaultsWorkspaceToConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rocketclaw.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
	  "openai": {"api_key": "test-key"},
	  "slack": {"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},
	  "mcp_external": {"enabled": true, "listen_addr": "127.0.0.1:8765"}
	}`), 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, dir, cfg.Workspace)
}

func TestLoadRejectsUnreadableOrInvalidConfig(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	require.ErrorContains(t, err, "read config")

	path := filepath.Join(t.TempDir(), "rocketclaw.json")
	require.NoError(t, os.WriteFile(path, []byte(`{`), 0o600))

	_, err = Load(path)
	require.ErrorContains(t, err, "parse config JSON")
}

func TestValidateRejectsMissingRequiredConfig(t *testing.T) {
	for _, tt := range []struct {
		name    string
		update  func(*Config)
		wantErr string
	}{
		{name: "workspace", update: func(c *Config) { c.Workspace = "" }, wantErr: "workspace is required"},
		{name: "rocketcode auth", update: func(c *Config) { c.OpenAI.RocketCodeAuth = "browser" }, wantErr: "openai.rocketcode_auth must be api_key or chatgpt"},
		{name: "provider rocketcode auth", update: func(c *Config) {
			c.Providers = map[string]OpenAIConfig{"work": {RocketCodeAuth: "browser"}}
		}, wantErr: `providers["work"].rocketcode_auth must be api_key or chatgpt`},
		{name: "provider API key", update: func(c *Config) {
			c.Providers = map[string]OpenAIConfig{"work": {}}
		}, wantErr: `providers["work"].api_key is required when providers["work"].rocketcode_auth is api_key`},
		{name: "mcp external listen addr", update: func(c *Config) { c.MCPExternal.Enabled = true }, wantErr: "mcp_external.listen_addr is required when mcp_external is enabled"},
		{name: "slack bot token", update: func(c *Config) { c.Slack.BotToken = "" }, wantErr: "slack.bot_token is required"},
		{name: "slack app token", update: func(c *Config) { c.Slack.AppToken = "" }, wantErr: "slack.app_token is required"},
		{name: "slack channels", update: func(c *Config) { c.Slack.Channels = nil }, wantErr: "slack.channels is required"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.update(cfg)

			err := cfg.Validate()
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestValidateSlackChannelsLegacyCoverage(t *testing.T) {
	cfg := validConfig()
	cfg.Slack.Channels = []SlackChannelConfig{
		{Channel: " triage ", Agents: []string{" planner ", "", "planner", "helper"}, AllowedUserIDs: []string{" U999 ", "", "U999"}},
		{Channel: " #team ", Agents: []string{" team "}, AllowedUserIDs: []string{" U123 ", "", "U123", "U456"}},
		{Channel: " ", Agents: []string{"ignored"}},
	}

	require.NoError(t, cfg.Validate())
	assert.Equal(t, []SlackChannelConfig{
		{Channel: "#triage", Agents: []string{"planner", "helper"}, AllowedUserIDs: []string{"U999"}},
		{Channel: "#team", Agents: []string{"team"}, AllowedUserIDs: []string{"U123", "U456"}},
	}, cfg.Slack.Channels)
}

func TestValidateSlackChannelsRejectsInvalidConfig(t *testing.T) {
	for _, tt := range []struct {
		name    string
		update  func(*[]SlackChannelConfig)
		wantErr string
	}{
		{name: "missing agents", update: func(channels *[]SlackChannelConfig) {
			*channels = []SlackChannelConfig{{Channel: "#triage", AllowedUserIDs: []string{"U123"}}}
		}, wantErr: "slack.channels[].agents is required"},
		{name: "missing channel allowlist", update: func(channels *[]SlackChannelConfig) {
			*channels = []SlackChannelConfig{{Channel: "#triage", Agents: []string{"triage"}}}
		}, wantErr: "slack.channels[].allowed_user_ids is required"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Slack.Channels = []SlackChannelConfig{{Channel: "#triage", Agents: []string{"triage"}, AllowedUserIDs: []string{"U123"}}}
			tt.update(&cfg.Slack.Channels)

			err := cfg.Validate()
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestValidateEnvironmentEntries(t *testing.T) {
	tests := []struct {
		name    string
		entry   string
		wantErr string
	}{
		{name: "missing separator", entry: "KEY", wantErr: `environment entry "KEY" must be in KEY=value form`},
		{name: "empty key", entry: " =value", wantErr: "environment keys must not be empty"},
		{name: "key NUL", entry: "KE\x00Y=value", wantErr: `environment key "KE\x00Y" must not contain NUL`},
		{name: "value NUL", entry: "KEY=va\x00lue", wantErr: `environment value for "KEY" must not contain NUL`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Environment = []string{tt.entry}

			err := cfg.Validate()
			require.EqualError(t, err, tt.wantErr)
		})
	}

	cfg := validConfig()
	cfg.Environment = []string{"KEY=value"}
	require.NoError(t, cfg.Validate())
}

func TestLoadExternalMCPUsers(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "rocketclaw.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{}`), 0o600))

	users, err := LoadExternalMCPUsers(configPath)
	require.NoError(t, err)
	assert.Nil(t, users)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "rocketclaw.users.json"), []byte(`{"admin":"secret"}`), 0o600))

	users, err = LoadExternalMCPUsers(configPath)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"admin": "secret"}, users)
}

func TestLoadExternalMCPUsersRejectsInvalidInputs(t *testing.T) {
	_, err := LoadExternalMCPUsers(" \t ")
	require.EqualError(t, err, "config path is required")

	dir := t.TempDir()
	fileParent := filepath.Join(dir, "not-dir")
	require.NoError(t, os.WriteFile(fileParent, []byte("file"), 0o600))

	_, err = LoadExternalMCPUsers(filepath.Join(fileParent, "rocketclaw.json"))
	require.ErrorContains(t, err, "stat external MCP users file")

	configPath := filepath.Join(dir, "rocketclaw.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{}`), 0o600))

	usersPath := filepath.Join(dir, "rocketclaw.users.json")
	require.NoError(t, os.WriteFile(usersPath, []byte(`{"admin":"secret"}`), 0o644))
	require.NoError(t, os.Chmod(usersPath, 0o644))

	_, err = LoadExternalMCPUsers(configPath)
	require.ErrorContains(t, err, "must have mode 0600")

	require.NoError(t, os.Chmod(usersPath, 0o600))
	require.NoError(t, os.WriteFile(usersPath, []byte(`null`), 0o600))

	_, err = LoadExternalMCPUsers(configPath)
	require.ErrorContains(t, err, "must be a JSON object")

	require.NoError(t, os.WriteFile(usersPath, []byte(`{`), 0o600))

	_, err = LoadExternalMCPUsers(configPath)
	require.ErrorContains(t, err, "parse external MCP users file")

	require.NoError(t, os.Remove(usersPath))
	require.NoError(t, os.Mkdir(usersPath, 0o600))

	_, err = LoadExternalMCPUsers(configPath)
	require.ErrorContains(t, err, "read external MCP users file")
}

func TestValidateAllowsChatGPTAuthWithoutAPIKey(t *testing.T) {
	cfg := validConfig()
	cfg.OpenAI.APIKey = ""
	cfg.OpenAI.RocketCodeAuth = "chatgpt"

	require.NoError(t, cfg.Validate())
	assert.Equal(t, "chatgpt", cfg.OpenAI.RocketCodeAuth)
}

func TestValidateRejectsAPIKeyAuthWithoutAPIKey(t *testing.T) {
	cfg := validConfig()
	cfg.OpenAI.APIKey = ""

	err := cfg.Validate()

	require.ErrorContains(t, err, "openai.api_key is required when openai.rocketcode_auth is api_key")
}

func loadTestConfig(t *testing.T, content string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rocketclaw.json")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644), "write config")
	cfg, err := Load(path)
	require.NoError(t, err)

	return cfg
}

func validConfig() *Config {
	cfg := new(Config)
	cfg.Workspace = "/tmp/project"
	cfg.Slack.BotToken = "xoxb-test"
	cfg.Slack.AppToken = "xapp-test"
	cfg.Slack.Channels = []SlackChannelConfig{{Channel: "#ops", Agents: []string{"main"}, AllowedUserIDs: []string{"U123"}}}
	cfg.OpenAI.APIKey = "test-key"

	return cfg
}
