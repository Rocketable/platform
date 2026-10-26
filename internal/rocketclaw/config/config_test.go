package config

import (
	"encoding/json"
	"fmt"
	"net/netip"
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

	var cfg Config
	require.NoError(t, json.Unmarshal(data, &cfg))
	require.NoError(t, cfg.Validate())
	require.NoError(t, cfg.ValidateBehavior())

	require.NotEmpty(t, cfg.Slack.Channels)
	assert.Equal(t, []string{"alice@example.com"}, cfg.Web.Admins)

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
	  },
	  "web": {"auto_settle_after": "1h"}
	}`)

	assert.Equal(t, "api_key", cfg.OpenAI.Auth)
	assert.Empty(t, cfg.AutoApproverModel)
	assert.Empty(t, cfg.WebUsers)
	assert.Equal(t, "0.0.0.0:3000", cfg.Web.ListenAddress)
	cfg.Web.ListenAddress = "127.0.0.1:8080"
	require.NoError(t, cfg.Validate())
	assert.Equal(t, "127.0.0.1:8080", cfg.Web.ListenAddress)
	assert.True(t, filepath.IsAbs(cfg.Workspace))
	assert.Empty(t, cfg.Logging.Level)
	require.NoError(t, cfg.ValidateBehavior())
	assert.Equal(t, "debug", cfg.Logging.Level)
}

func TestLoadAcceptsSetupOnlyFile(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "openai": {"api_key": "test-key"},
	  "slack": {"bot_token": "xoxb-test", "app_token": "xapp-test"},
	  "web": {"admins": ["alice@example.com"]}
	}`)

	assert.Empty(t, cfg.Slack.Channels)
	assert.Empty(t, cfg.Models)
	assert.Equal(t, []string{"alice@example.com"}, cfg.Web.Admins)
	require.EqualError(t, cfg.ValidateBehavior(), "slack.channels is required")
}

func TestBehaviorRoundTrip(t *testing.T) {
	document := `{
	  "models": {"fast": "gpt-6.1-luna"},
	  "auto_approver_model": "work/gpt-6.1-sol",
	  "slack": {"channels": [{"channel": "#ops", "agents": ["main"], "allowed_user_ids": ["U123"]}]},
	  "web_users": {"192.0.2.1": "alice"},
	  "server_display_name": "home lab",
	  "logging": {"level": "warn"},
	  "instrumentation": {"hide_inputs": true, "hide_outputs": true},
	  "web": {"sentry": {"environment": "production", "traces_sample_rate": 0.25}},
	  "overlays": ["github.com/rocketable/overlay1@main"]
	}`

	var behavior Behavior
	require.NoError(t, json.Unmarshal([]byte(document), &behavior))

	var file Config
	require.NoError(t, json.Unmarshal([]byte(document), &file))
	require.Equal(t, behavior, file.Behavior(), "the document has the file's shape")

	cfg := validConfig()
	cfg.Web.Sentry.DSN = "https://public@o1.ingest.sentry.io/1"
	cfg.Instrumentation.CollectorEndpoint = "http://localhost:6006"
	cfg.SetBehavior(&behavior)

	require.Equal(t, behavior, cfg.Behavior())
	data, err := json.Marshal(cfg.Behavior())
	require.NoError(t, err)
	require.JSONEq(t, document, string(data))
	assert.Equal(t, "xoxb-test", cfg.Slack.BotToken)
	assert.Equal(t, "xapp-test", cfg.Slack.AppToken)
	assert.Equal(t, "https://public@o1.ingest.sentry.io/1", cfg.Web.Sentry.DSN)
	assert.Equal(t, "http://localhost:6006", cfg.Instrumentation.CollectorEndpoint)
}

func TestAttachmentLocation(t *testing.T) {
	for _, runtimeDir := range []string{".rocketclaw", ".femtoclaw"} {
		for _, test := range []struct {
			name, raw, location string
			driver              AttachmentDriver
		}{
			{"default", `{}`, filepath.Join(runtimeDir, "attachments"), FilesystemAttachments},
			{"relative", `{"driver":"filesystem","path":"originals"}`, "originals", FilesystemAttachments},
			{"absolute", `{"driver":"filesystem","path":"/srv/originals"}`, "/srv/originals", FilesystemAttachments},
			{"s3", `{"driver":"s3","bucket_arn":"arn:aws:s3:::originals"}`, "originals", S3Attachments},
			{"unknown", `{"driver":"postgres"}`, "", ""},
			{"missing bucket", `{"driver":"s3"}`, "", ""},
			{"object arn", `{"driver":"s3","bucket_arn":"arn:aws:s3:::originals/file"}`, "", ""},
			{"access point arn", `{"driver":"s3","bucket_arn":"arn:aws:s3:us-east-1:123456789012:accesspoint/name"}`, "", ""},
			{"filesystem bucket", `{"driver":"filesystem","bucket_arn":"arn:aws:s3:::originals"}`, "", ""},
			{"s3 path", `{"driver":"s3","bucket_arn":"arn:aws:s3:::originals","path":"originals"}`, "", ""},
		} {
			t.Run(runtimeDir+"/"+test.name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "config.json")
				body := fmt.Sprintf(`{"workspace":"work","database_url":"postgres://localhost/test","openai":{"api_key":"test"},"slack":{"bot_token":"test","app_token":"test","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},"attachments":%s}`, test.raw)
				require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

				cfg, err := Load(path, "", AWSFetcher{})
				if test.driver == "" {
					require.Error(t, err)
					return
				}

				require.NoError(t, err)

				cfg.WorkDir = runtimeDir
				driver, location, err := cfg.AttachmentLocation()
				require.NoError(t, err)
				require.Equal(t, test.driver, driver)

				want := test.location
				if driver == FilesystemAttachments && !filepath.IsAbs(want) {
					want = filepath.Join(filepath.Dir(path), "work", want)
				}

				require.Equal(t, want, location)
			})
		}
	}
}

func TestLoadSentry(t *testing.T) {
	for _, tt := range []struct {
		name, sentry, wantErr string
	}{
		{"disabled", `{}`, ""},
		{"configured", `{"dsn":"https://public@o1.ingest.sentry.io/1","environment":"production","traces_sample_rate":0.25}`, ""},
		{"zero sampling", `{"dsn":"https://public@o1.ingest.sentry.io/1","traces_sample_rate":0}`, ""},
		{"invalid DSN", `{"dsn":"not-a-dsn"}`, "web.sentry.dsn"},
		{"private key", `{"dsn":"https://public:private@o1.ingest.sentry.io/1"}`, "web.sentry.dsn"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rocketclaw.json")
			content := fmt.Sprintf(`{"database_url":"postgres://localhost/test","openai":{"api_key":"test"},"slack":{"bot_token":"test","app_token":"test","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},"web":{"sentry":%s}}`, tt.sentry)
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

			cfg, err := Load(path, "", AWSFetcher{})
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			data, err := json.Marshal(cfg.Web.Sentry)
			require.NoError(t, err)
			require.JSONEq(t, tt.sentry, string(data))
		})
	}
}

func TestLoadTsnet(t *testing.T) {
	for _, tt := range []struct {
		name, web string
		want      *TsnetConfig
		wantErr   bool
	}{
		{name: "off", web: `{}`},
		{name: "default hostname", web: `{"tsnet":{}}`, want: &TsnetConfig{Hostname: "rocketclaw"}},
		{name: "configured", web: `{"tsnet":{"hostname":"phone-1","tags":["tag:rocketclaw"],"origin":"https://evil.example"}}`, want: &TsnetConfig{Hostname: "phone-1", Tags: []string{"tag:rocketclaw"}}},
		{name: "path hostname", web: `{"tsnet":{"hostname":"../state"}}`, wantErr: true},
		{name: "untagged tag", web: `{"tsnet":{"tags":["rocketclaw"]}}`, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rocketclaw.json")
			content := `{"database_url":"postgres://localhost/test","openai":{"api_key":"test"},"slack":{"bot_token":"test","app_token":"test","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},"web":` + tt.web + `}`
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

			cfg, err := Load(path, "", AWSFetcher{})
			if tt.wantErr {
				require.ErrorContains(t, err, "web.tsnet")
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, cfg.Web.Tsnet)
		})
	}
}

func TestLoadPreservesModelConfig(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "models": {"coding-high": "software-development-sol", "review-fast": "gpt-5.6-luna"},
	  "auto_approver_model": " openai/gpt-6.1-sol ",
	  "openai": {"api_key": "test-key"},
	  "slack": {"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},
	  "mcp_external": {"enabled": true, "listen_addr": "127.0.0.1:8765"}
	}`)

	behavior := cfg.Behavior()
	assert.Equal(t, " openai/gpt-6.1-sol ", behavior.AutoApproverModel)
	assert.Equal(t, map[string]string{"coding-high": "software-development-sol", "review-fast": "gpt-5.6-luna"}, behavior.Models)
	require.NoError(t, cfg.ValidateBehavior())
	assert.Equal(t, "gpt-6.1-sol", cfg.AutoApproverModel)
}

func TestValidateBehaviorWebUsers(t *testing.T) {
	for _, tt := range []struct {
		name, users, wantErr string
	}{
		{name: "mapped", users: `{"192.0.2.1":"alice","2001:db8::1":"bob"}`},
		{name: "empty", users: `{}`},
		{name: "invalid IP", users: `{"alice":"alice"}`, wantErr: "parse config JSON"},
		{name: "empty IP", users: `{"":"alice"}`, wantErr: "web_users requires valid IP keys and non-empty, trimmed usernames"},
		{name: "empty username", users: `{"192.0.2.1":""}`, wantErr: "web_users requires valid IP keys and non-empty, trimmed usernames"},
		{name: "untrimmed username", users: `{"192.0.2.1":" alice "}`, wantErr: "web_users requires valid IP keys and non-empty, trimmed usernames"},
		{name: "nonstring username", users: `{"192.0.2.1":123}`, wantErr: "parse config JSON"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rocketclaw.json")
			content := fmt.Sprintf(`{"database_url":"postgres://localhost/rocketclaw_test", "openai":{"api_key":"test"}, "slack":{"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]}, "web_users":%s}`, tt.users)
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

			cfg, err := Load(path, "", AWSFetcher{})
			if err == nil {
				err = cfg.ValidateBehavior()
			}

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)

			if tt.name == "mapped" {
				require.Equal(t, map[netip.Addr]string{netip.MustParseAddr("192.0.2.1"): "alice", netip.MustParseAddr("2001:db8::1"): "bob"}, cfg.WebUsers)
			} else {
				require.Empty(t, cfg.WebUsers)
			}
		})
	}
}

func TestLoadPreservesNamedProviders(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "openai": {"api_key": "default-key"},
	  "providers": {
	    "work": {"api_key": "work-key", "api_base_url": "https://work.example/v1"},
	    "chat": {"rocketcode_auth": " chatgpt "},
	    "renamed": {"auth": "chatgpt", "rocketcode_auth": "api_key"}
	  },
	  "slack": {"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]}
	}`)

	provider, ok := cfg.Provider("openai")
	require.True(t, ok)
	assert.Equal(t, "default-key", provider.APIKey)
	assert.Equal(t, "api_key", provider.Auth)

	provider, ok = cfg.Provider("work")
	require.True(t, ok)
	assert.Equal(t, OpenAIConfig{APIKey: "work-key", APIBaseURL: "https://work.example/v1", Auth: "api_key"}, provider)

	assert.Zero(t, cfg.OpenAI.AutocompactionThreshold)
	assert.Zero(t, provider.AutocompactionThreshold)

	provider, ok = cfg.Provider("chat")
	require.True(t, ok)
	assert.Equal(t, "chatgpt", provider.Auth)

	provider, ok = cfg.Provider("renamed")
	require.True(t, ok)
	assert.Equal(t, OpenAIConfig{Auth: "chatgpt"}, provider)

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
				"zulu":  {Auth: "browser"},
				"alpha": {Auth: "session"},
			},
			wantErr: `providers["alpha"].auth must be api_key or chatgpt`,
		},
		{
			name:      "API key",
			providers: map[string]OpenAIConfig{"work": {}},
			wantErr:   `providers["work"].api_key is required when providers["work"].auth is api_key`,
		},
		{
			name:      "autocompaction threshold",
			providers: map[string]OpenAIConfig{"work": {APIKey: "work-key", AutocompactionThreshold: -1}},
			wantErr:   `providers["work"].autocompaction_threshold must be a positive integer`,
		},
		{
			name:      "prompt cache marker position",
			providers: map[string]OpenAIConfig{"work": {APIKey: "work-key", PromptCacheMarkersRelativePosition: new(0)}},
			wantErr:   `providers["work"].prompt_cache_markers_relative_position must be a negative integer`,
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
		{model: "gpt-6.1-sol", want: "gpt-6.1-sol"},
		{model: " openai/gpt-6.1-sol ", want: "gpt-6.1-sol"},
		{model: " work/gpt-6.1-sol ", want: "work/gpt-6.1-sol"},
		{model: "anthropic/api/claude-sonnet-5-5", want: "anthropic/api/claude-sonnet-5-5"},
		{model: "/", wantErr: "expected model or provider/model"},
		{model: "work//model", wantErr: "expected model or provider/model"},
		{model: "work/model/", wantErr: "expected model or provider/model"},
	} {
		t.Run(tt.model, func(t *testing.T) {
			cfg := validConfig()
			cfg.AutoApproverModel = tt.model

			err := cfg.ValidateBehavior()
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
		{name: "empty name", models: map[string]string{" ": "gpt-6.1-sol"}},
		{name: "empty model", models: map[string]string{"coding-high": " "}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Models = tt.models

			err := cfg.ValidateBehavior()
			require.EqualError(t, err, "models keys and values must not be empty")
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
		{name: "concrete", model: "gpt-6.1-sol", want: "gpt-6.1-sol"},
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

	got, err := cfg.RenderAgentModel(" work/gpt-6.1-sol ")
	require.NoError(t, err)
	assert.Equal(t, "work/gpt-6.1-sol", got)
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

func TestValidateRejectsEnabledInstrumentationWithoutDestination(t *testing.T) {
	cfg := validConfig()
	cfg.Instrumentation.Enabled = true

	err := cfg.Validate()

	require.ErrorContains(t, err, "instrumentation.collector_endpoint or posthog.project_token is required")
}

func TestValidatePostHog(t *testing.T) {
	cfg := validConfig()
	cfg.Instrumentation.Enabled = true
	cfg.PostHog.ProjectToken = "phc_project"

	require.NoError(t, cfg.Validate())
	assert.Equal(t, "https://us.i.posthog.com", cfg.PostHog.Host)

	cfg.PostHog = PostHogConfig{ProjectToken: "phc_project", Host: "https://eu.i.posthog.com/"}
	require.NoError(t, cfg.Validate())
	assert.Equal(t, "https://eu.i.posthog.com", cfg.PostHog.Host)

	cfg.PostHog.ProjectToken = "phx_personal"
	require.ErrorContains(t, cfg.Validate(), "posthog.project_token must be a public project token")
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
			name: "name exceeds length limit",
			servers: map[string]MCPServerConfig{
				strings.Repeat("a", 129): {Command: "npx"},
			},
			wantErr: "name exceeds maximum length of 128 characters (current: 129)",
		},
		{
			name: "name at length limit",
			servers: map[string]MCPServerConfig{
				strings.Repeat("a", 128): {Command: "npx"},
			},
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

func TestValidateOpenCode(t *testing.T) {
	valid := OpenCodeConfig{URL: "https://box.example:4096", Password: "token"}

	for _, tt := range []struct {
		name    string
		mutate  func(*OpenCodeConfig)
		key     string
		wantErr string
	}{
		{name: "valid", mutate: func(*OpenCodeConfig) {}, key: "box"},
		{name: "bad scheme", mutate: func(s *OpenCodeConfig) { s.URL = "ftp://box" }, key: "box", wantErr: `opencode["box"]: url must be an http or https URL`},
		{name: "empty password", mutate: func(s *OpenCodeConfig) { s.Password = "" }, key: "box", wantErr: `opencode["box"]: password is required`},
		{name: "bad name", mutate: func(*OpenCodeConfig) {}, key: "box:1", wantErr: `opencode["box:1"]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := valid
			tt.mutate(&server)

			cfg := validConfig()
			cfg.OpenCode = map[string]OpenCodeConfig{tt.key: server}

			err := cfg.Validate()
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, cfg.OpenCode, clone(cfg).OpenCode)
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

	require.NoError(t, cfg.ValidateBehavior())
	assert.Equal(t, []string{"github.com/rocketable/overlay1@main", "github.com/rocketable/overlay2"}, cfg.Overlays)
}

func TestLoadDefaultsWorkspaceToConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rocketclaw.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
	  "database_url": "postgres://localhost/rocketclaw_test?sslmode=disable",
	  "openai": {"api_key": "test-key"},
	  "slack": {"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},
	  "mcp_external": {"enabled": true, "listen_addr": "127.0.0.1:8765"},
	  "opencode": {"box": {"url": "https://box.example:4096", "password": "pw", "port": 4100}}
	}`), 0o600))

	cfg, err := Load(path, "", AWSFetcher{})
	require.NoError(t, err, "a config written for relay ports still loads")
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
		{name: "mcp external listen addr", update: func(c *Config) { c.MCPExternal.Enabled = true }, wantErr: "mcp_external.listen_addr is required when mcp_external is enabled"},
		{name: "slack bot token", update: func(c *Config) { c.Slack.BotToken = "" }, wantErr: "slack.bot_token is required"},
		{name: "slack app token", update: func(c *Config) { c.Slack.AppToken = "" }, wantErr: "slack.app_token is required"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.update(cfg)

			err := cfg.Validate()
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestValidateBehaviorRejectsInvalidSettings(t *testing.T) {
	for _, tt := range []struct {
		name    string
		update  func(*Config)
		wantErr string
	}{
		{name: "missing model", update: func(c *Config) { c.AutoApproverModel = "work/" }, wantErr: `auto_approver_model: invalid model "work/": expected model or provider/model`},
		{name: "slack channels", update: func(c *Config) { c.Slack.Channels = nil }, wantErr: "slack.channels is required"},
		{name: "negative sampling", update: func(c *Config) { c.Web.Sentry.TracesSampleRate = new(-0.1) }, wantErr: "web.sentry.traces_sample_rate must be between 0 and 1"},
		{name: "excessive sampling", update: func(c *Config) { c.Web.Sentry.TracesSampleRate = new(1.1) }, wantErr: "web.sentry.traces_sample_rate must be between 0 and 1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.update(cfg)

			require.NoError(t, cfg.Validate(), "setup validation ignores behavior")
			require.EqualError(t, cfg.ValidateBehavior(), tt.wantErr)
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

	require.NoError(t, cfg.ValidateBehavior())
	assert.Equal(t, []SlackChannelConfig{
		{Channel: "#triage", Agents: []string{"planner", "helper"}, AllowedUserIDs: []string{"U999"}},
		{Channel: "#team", Agents: []string{"team"}, AllowedUserIDs: []string{"U123", "U456"}},
	}, cfg.Slack.Channels)
	triage, ok := cfg.Slack.Channel("#triage")
	require.True(t, ok)
	assert.Equal(t, []string{"planner", "helper"}, triage.Agents)

	_, ok = cfg.Slack.Channel("#missing")
	assert.False(t, ok)
}

func TestValidateSlackKeepsAtChannel(t *testing.T) {
	cfg := validConfig()
	cfg.Slack.Channels = []SlackChannelConfig{
		{Channel: " @ ", Agents: []string{"main"}, AllowedUserIDs: []string{"U1"}},
	}

	require.NoError(t, cfg.ValidateBehavior())
	assert.Equal(t, []SlackChannelConfig{
		{Channel: "@", Agents: []string{"main"}, AllowedUserIDs: []string{"U1"}},
	}, cfg.Slack.Channels)
	at, ok := cfg.Slack.Channel("@")
	require.True(t, ok)
	assert.Equal(t, []string{"main"}, at.Agents)
}

func TestValidateSlackLeavesHashAtUnchanged(t *testing.T) {
	cfg := validConfig()
	cfg.Slack.Channels = []SlackChannelConfig{
		{Channel: "#@", Agents: []string{"main"}, AllowedUserIDs: []string{"U1"}},
	}

	require.NoError(t, cfg.ValidateBehavior())
	assert.Equal(t, "#@", cfg.Slack.Channels[0].Channel)
}

func TestValidateSlackRejectsSecondAtChannel(t *testing.T) {
	cfg := validConfig()
	cfg.Slack.Channels = []SlackChannelConfig{
		{Channel: "@", Agents: []string{"main"}, AllowedUserIDs: []string{"U1"}},
		{Channel: "@", Agents: []string{"other"}, AllowedUserIDs: []string{"U2"}},
	}

	require.ErrorContains(t, cfg.ValidateBehavior(), "slack.channels may include only one @ entry")
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

			err := cfg.ValidateBehavior()
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

func TestValidateRejectsAPIKeyAuthWithoutAPIKey(t *testing.T) {
	cfg := validConfig()
	cfg.OpenAI.APIKey = ""

	err := cfg.Validate()

	require.ErrorContains(t, err, "openai.api_key is required when openai.auth is api_key")
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

func TestWebCertificateNeedsBothFiles(t *testing.T) {
	for _, web := range []WebConfig{{TLSCertFile: "cert.pem"}, {TLSKeyFile: "key.pem"}} {
		cfg := &Config{Workspace: t.TempDir(), Web: web}
		require.ErrorContains(t, cfg.Validate(), "web.tls_cert_file and web.tls_key_file")
	}
}
