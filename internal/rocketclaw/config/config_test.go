package config

import (
	"encoding/json"
	"fmt"
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
	provider, ok := cfg.Provider("work")
	require.True(t, ok)
	assert.Equal(t, "https://work.example/v1", provider.APIBaseURL)
	assert.Equal(t, "set-me", provider.APIKey)
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

func TestUsernameForIP(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "users": {"alice": "100.64.0.1"},
	  "openai": {"api_key": "test-key"},
	  "slack": {
	    "bot_token": "xoxb-test",
	    "app_token": "xapp-test",
	    "channels": [{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]
	  }
	}`)

	name, ok := cfg.UsernameForIP("100.64.0.1")
	require.True(t, ok)
	require.Equal(t, "alice", name)

	name, ok = cfg.UsernameForIP("::ffff:100.64.0.1")
	require.True(t, ok)
	require.Equal(t, "alice", name)

	_, ok = cfg.UsernameForIP("8.8.8.8")
	require.False(t, ok)
}

func TestLoadIgnoresFormerMCPDevelopment(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "openai": {"api_key": "test-key"},
	  "slack": {
	    "bot_token": "xoxb-test",
	    "app_token": "xapp-test",
	    "channels": [{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]
	  },
	  "mcp_development": {"enabled": true, "listen_addr": "127.0.0.1:8766"}
	}`)

	assert.Equal(t, "api_key", cfg.OpenAI.RocketCodeAuth)
}

func TestLoadPreservesModelConfig(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "models": {"coding-high": "software-development-sol", "review-fast": "gpt-5.6-luna"},
	  "auto_approver_model": " openai/gpt-5.5 ",
	  "openai": {"api_key": "test-key"},
	  "slack": {"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},
	  "mcp_external": {"enabled": true, "listen_addr": "127.0.0.1:8765"}
	}`)

	assert.Equal(t, "gpt-5.5", cfg.AutoApproverModel)
	assert.Equal(t, map[string]string{"coding-high": "software-development-sol", "review-fast": "gpt-5.6-luna"}, cfg.Models)
}

func TestLoadPreservesNamedProviders(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "openai": {"api_key": "default-key"},
	  "providers": {
	    "work": {"api_key": "work-key", "api_base_url": "https://work.example/v1"},
	    "chat": {"rocketcode_auth": " chatgpt "}
	  },
	  "slack": {"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]}
	}`)

	provider, ok := cfg.Provider("openai")
	require.True(t, ok)
	assert.Equal(t, "default-key", provider.APIKey)
	assert.Equal(t, "api_key", provider.RocketCodeAuth)

	provider, ok = cfg.Provider("work")
	require.True(t, ok)
	assert.Equal(t, OpenAIConfig{APIKey: "work-key", APIBaseURL: "https://work.example/v1", RocketCodeAuth: "api_key"}, provider)

	assert.Zero(t, cfg.OpenAI.AutocompactionThreshold)
	assert.Zero(t, provider.AutocompactionThreshold)

	provider, ok = cfg.Provider("chat")
	require.True(t, ok)
	assert.Equal(t, "chatgpt", provider.RocketCodeAuth)

	_, ok = cfg.Provider("missing")
	assert.False(t, ok)
}

func TestLoadPreservesAutocompactionThreshold(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "openai": {"api_key": "default-key", "autocompaction_threshold": 150000},
	  "providers": {
	    "work": {"api_key": "work-key", "autocompaction_threshold": 80000}
	  },
	  "slack": {"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]}
	}`)

	assert.Equal(t, int64(150000), cfg.OpenAI.AutocompactionThreshold)

	provider, ok := cfg.Provider("work")
	require.True(t, ok)
	assert.Equal(t, int64(80000), provider.AutocompactionThreshold)
}

func TestValidateRejectsInvalidProviderNames(t *testing.T) {
	for _, name := range []string{"", " ", " work", "work ", "work/team", "openai"} {
		t.Run(name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Providers = map[string]OpenAIConfig{name: {APIKey: "work-key"}}

			err := cfg.Validate()
			require.ErrorContains(t, err, fmt.Sprintf("providers[%q]", name))
		})
	}
}

func TestValidateReportsNamedProviderFieldErrors(t *testing.T) {
	for _, tt := range []struct {
		name      string
		providers map[string]OpenAIConfig
		wantErr   string
	}{
		{
			name: "auth",
			providers: map[string]OpenAIConfig{
				"zulu":  {RocketCodeAuth: "browser"},
				"alpha": {RocketCodeAuth: "session"},
			},
			wantErr: `providers["alpha"].rocketcode_auth must be api_key or chatgpt`,
		},
		{
			name:      "API key",
			providers: map[string]OpenAIConfig{"work": {}},
			wantErr:   `providers["work"].api_key is required when providers["work"].rocketcode_auth is api_key`,
		},
		{
			name:      "autocompaction threshold",
			providers: map[string]OpenAIConfig{"work": {APIKey: "work-key", AutocompactionThreshold: -1}},
			wantErr:   `providers["work"].autocompaction_threshold must be a positive integer`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Providers = tt.providers

			err := cfg.Validate()
			require.EqualError(t, err, tt.wantErr)
		})
	}
}

func TestValidateAutoApproverModels(t *testing.T) {
	for _, tt := range []struct {
		model   string
		want    string
		wantErr string
	}{
		{model: "gpt-5.5", want: "gpt-5.5"},
		{model: " openai/gpt-5.5 ", want: "gpt-5.5"},
		{model: " work/gpt-5.5 ", want: "work/gpt-5.5"},
		{model: "/", wantErr: "expected model or provider/model"},
		{model: "work/model/extra", wantErr: "expected model or provider/model"},
	} {
		t.Run(tt.model, func(t *testing.T) {
			cfg := validConfig()
			cfg.AutoApproverModel = tt.model

			err := cfg.Validate()
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.AutoApproverModel)
		})
	}
}

func TestValidateRejectsInvalidModels(t *testing.T) {
	for _, tt := range []struct {
		name   string
		models map[string]string
	}{
		{name: "empty name", models: map[string]string{" ": "gpt-5.5"}},
		{name: "empty model", models: map[string]string{"coding-high": " "}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Models = tt.models

			err := cfg.Validate()
			require.ErrorContains(t, err, "models")
		})
	}
}

func TestRenderAgentModel(t *testing.T) {
	cfg := validConfig()
	cfg.Models = map[string]string{
		"coding-high":        "software-development-sol",
		`quoted"placeholder`: "gpt-5.6-luna",
		"nested":             `{{ model "coding-high" }}`,
		"invalid":            "anthropic/claude",
	}
	require.NoError(t, cfg.Validate())

	for _, tt := range []struct {
		name    string
		model   string
		want    string
		wantErr string
	}{
		{name: "concrete", model: "gpt-5.5", want: "gpt-5.5"},
		{name: "placeholder", model: `{{ model "coding-high" }}`, want: "software-development-sol"},
		{name: "arbitrary quoted name", model: `{{ model "quoted\"placeholder" }}`, want: "gpt-5.6-luna"},
		{name: "missing", model: `{{ model "missing" }}`, wantErr: `model "missing" is not configured`},
		{name: "invalid template", model: `{{ model "coding-high" }`, wantErr: "parse model template"},
		{name: "empty result", model: `{{ "" }}`, wantErr: "model template returned an empty model"},
		{name: "nested", model: `{{ model "nested" }}`, wantErr: "model template returned another template"},
		{name: "provider validation is separate", model: `{{ model "invalid" }}`, want: "anthropic/claude"},
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

func TestRenderAgentModelPreservesNamedProvider(t *testing.T) {
	cfg := validConfig()

	got, err := cfg.RenderAgentModel(" work/gpt-5.5 ")
	require.NoError(t, err)
	assert.Equal(t, "work/gpt-5.5", got)
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

func TestValidateMCPServers(t *testing.T) {
	for _, tt := range []struct {
		name    string
		servers map[string]MCPServerConfig
		want    map[string]MCPServerConfig
		wantErr string
	}{
		{
			name: "both command and url",
			servers: map[string]MCPServerConfig{
				"github": {Command: "npx", URL: "https://example.com/mcp"},
			},
			wantErr: `mcp_servers["github"]: set exactly one of command or url`,
		},
		{
			name: "neither command nor url",
			servers: map[string]MCPServerConfig{
				"github": {},
			},
			wantErr: `mcp_servers["github"]: set exactly one of command or url`,
		},
		{
			name: "empty command",
			servers: map[string]MCPServerConfig{
				"github": {Command: "  "},
			},
			wantErr: `mcp_servers["github"]: set exactly one of command or url`,
		},
		{
			name: "empty name",
			servers: map[string]MCPServerConfig{
				"": {Command: "npx"},
			},
			wantErr: `mcp_servers[""]: name cannot be empty`,
		},
		{
			name: "invalid characters",
			servers: map[string]MCPServerConfig{
				"git hub": {Command: "npx"},
			},
			wantErr: `mcp_servers["git hub"]: name contains invalid characters`,
		},
		{
			name: "dash and underscore names (MCP charset)",
			servers: map[string]MCPServerConfig{
				"git-hub":             {Command: "npx"},
				"sequentialthinking":  {Command: "npx"},
				"Sequential-Thinking": {URL: "https://example.com/mcp"},
				"my_server.v2":        {Command: "npx"},
			},
			want: map[string]MCPServerConfig{
				"git-hub":             {Command: "npx"},
				"sequentialthinking":  {Command: "npx"},
				"Sequential-Thinking": {URL: "https://example.com/mcp"},
				"my_server.v2":        {Command: "npx"},
			},
		},
		{
			name: "bad url scheme",
			servers: map[string]MCPServerConfig{
				"remote": {URL: "ftp://example.com/mcp"},
			},
			wantErr: `mcp_servers["remote"]: url must be an http or https URL`,
		},
		{
			name: "stdio with args env cwd",
			servers: map[string]MCPServerConfig{
				"filesystem": {
					Command: " npx ",
					Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
					Env:     map[string]string{"NODE_ENV": "production"},
					Cwd:     " work ",
				},
			},
			want: map[string]MCPServerConfig{
				"filesystem": {
					Command: "npx",
					Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
					Env:     map[string]string{"NODE_ENV": "production"},
					Cwd:     "work",
				},
			},
		},
		{
			name: "http with headers",
			servers: map[string]MCPServerConfig{
				"remote": {
					URL:     " https://mcp.example.com/mcp ",
					Headers: map[string]string{"Authorization": "Bearer set-me"},
				},
			},
			want: map[string]MCPServerConfig{
				"remote": {
					URL:     "https://mcp.example.com/mcp",
					Headers: map[string]string{"Authorization": "Bearer set-me"},
				},
			},
		},
		{
			name:    "omitted",
			servers: nil,
		},
		{
			name:    "empty map",
			servers: map[string]MCPServerConfig{},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.MCPServers = tt.servers

			err := cfg.Validate()
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)

			if tt.want != nil {
				assert.Equal(t, tt.want, cfg.MCPServers)
			}
		})
	}
}

func TestLoadOmitsMCPServers(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "openai": {"api_key": "test-key"},
	  "slack": {"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]}
	}`)

	assert.Nil(t, cfg.MCPServers)
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
	  "database_url": "postgres://localhost/rocketclaw_test?sslmode=disable",
	  "openai": {"api_key": "test-key"},
	  "slack": {"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},
	  "mcp_external": {"enabled": true, "listen_addr": "127.0.0.1:8765"}
	}`), 0o600))

	cfg, err := Load(path, "", AWSFetcher{})
	require.NoError(t, err)
	assert.Equal(t, dir, cfg.Workspace)
}

func TestLoadRejectsUnreadableOrInvalidConfig(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.json"), "", AWSFetcher{})
	require.ErrorContains(t, err, "read config")

	path := filepath.Join(t.TempDir(), "rocketclaw.json")
	require.NoError(t, os.WriteFile(path, []byte(`{`), 0o600))

	_, err = Load(path, "", AWSFetcher{})
	require.ErrorContains(t, err, "parse config JSON")
}

func TestValidateRejectsMissingRequiredConfig(t *testing.T) {
	for _, tt := range []struct {
		name    string
		update  func(*Config)
		wantErr string
	}{
		{name: "workspace", update: func(c *Config) { c.Workspace = "" }, wantErr: "workspace is required"},
		{name: "database url", update: func(c *Config) { c.DatabaseURL = "" }, wantErr: "database_url is required"},
		{name: "rocketcode auth", update: func(c *Config) { c.OpenAI.RocketCodeAuth = "browser" }, wantErr: "openai.rocketcode_auth must be api_key or chatgpt"},
		{name: "autocompaction threshold", update: func(c *Config) { c.OpenAI.AutocompactionThreshold = -1 }, wantErr: "openai.autocompaction_threshold must be a positive integer"},
		{name: "missing provider", update: func(c *Config) { c.AutoApproverModel = "/model" }, wantErr: `auto_approver_model: invalid model "/model": expected model or provider/model`},
		{name: "missing model", update: func(c *Config) { c.AutoApproverModel = "work/" }, wantErr: `auto_approver_model: invalid model "work/": expected model or provider/model`},
		{name: "extra empty part", update: func(c *Config) { c.AutoApproverModel = "work//model" }, wantErr: `auto_approver_model: invalid model "work//model": expected model or provider/model`},
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

func TestValidateSlackKeepsAtChannel(t *testing.T) {
	cfg := validConfig()
	cfg.Slack.Channels = []SlackChannelConfig{
		{Channel: " @ ", Agents: []string{"main"}, AllowedUserIDs: []string{"U1"}},
	}

	require.NoError(t, cfg.Validate())
	assert.Equal(t, []SlackChannelConfig{
		{Channel: "@", Agents: []string{"main"}, AllowedUserIDs: []string{"U1"}},
	}, cfg.Slack.Channels)
}

func TestValidateSlackLeavesHashAtUnchanged(t *testing.T) {
	cfg := validConfig()
	cfg.Slack.Channels = []SlackChannelConfig{
		{Channel: "#@", Agents: []string{"main"}, AllowedUserIDs: []string{"U1"}},
	}

	require.NoError(t, cfg.Validate())
	assert.Equal(t, "#@", cfg.Slack.Channels[0].Channel)
}

func TestValidateSlackRejectsSecondAtChannel(t *testing.T) {
	cfg := validConfig()
	cfg.Slack.Channels = []SlackChannelConfig{
		{Channel: "@", Agents: []string{"main"}, AllowedUserIDs: []string{"U1"}},
		{Channel: "@", Agents: []string{"other"}, AllowedUserIDs: []string{"U2"}},
	}

	require.ErrorContains(t, cfg.Validate(), "slack.channels may include only one @ entry")
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
		{name: "missing at agents", update: func(channels *[]SlackChannelConfig) {
			*channels = []SlackChannelConfig{{Channel: "@", AllowedUserIDs: []string{"U1"}}}
		}, wantErr: "slack.channels[].agents is required"},
		{name: "missing at allowlist", update: func(channels *[]SlackChannelConfig) {
			*channels = []SlackChannelConfig{{Channel: "@", Agents: []string{"main"}}}
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

	if !strings.Contains(content, `"database_url"`) {
		content = strings.Replace(content, "{", `{"database_url":"postgres://localhost/rocketclaw_test?sslmode=disable",`, 1)
	}

	path := filepath.Join(t.TempDir(), "rocketclaw.json")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644), "write config")
	cfg, err := Load(path, "", AWSFetcher{})
	require.NoError(t, err)

	return cfg
}

func validConfig() *Config {
	cfg := new(Config)
	cfg.Workspace = "/tmp/project"
	cfg.DatabaseURL = "postgres://localhost/rocketclaw_test?sslmode=disable"
	cfg.Slack.BotToken = "xoxb-test"
	cfg.Slack.AppToken = "xapp-test"
	cfg.Slack.Channels = []SlackChannelConfig{{Channel: "#ops", Agents: []string{"main"}, AllowedUserIDs: []string{"U123"}}}
	cfg.OpenAI.APIKey = "test-key"

	return cfg
}
