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
	  "web": {"auto_settle_after": "1h", "listen_address": "127.0.0.1:8080", "tls_cert_file": "cert.pem", "tsnet": {}}
	}`)

	assert.Equal(t, "api_key", cfg.OpenAI.Auth)
	assert.Empty(t, cfg.AutoApproverModel)
	assert.Equal(t, "rocketclaw", cfg.Web.Tsnet.Hostname)
	assert.True(t, filepath.IsAbs(cfg.Workspace))
	assert.Empty(t, cfg.Logging.Level)
	require.NoError(t, cfg.ValidateBehavior())
	assert.Equal(t, "debug", cfg.Logging.Level)
}

func TestLoadAcceptsSetupOnlyFile(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "openai": {"api_key": "test-key"},
	  "slack": {"bot_token": "xoxb-test", "app_token": "xapp-test"},
	  "web": {"admins": ["alice@example.com"], "tsnet": {}}
	}`)

	assert.Empty(t, cfg.Slack.Channels)
	assert.Equal(t, []string{"alice@example.com"}, cfg.Web.Admins)
	require.EqualError(t, cfg.ValidateBehavior(), "slack.channels is required")
}

func TestValidateAcceptsConfigWithoutSlack(t *testing.T) {
	cfg := validConfig()
	cfg.Slack = SlackConfig{}
	require.NoError(t, cfg.Validate())
	require.NoError(t, cfg.ValidateBehavior())
}

func TestBehaviorRoundTrip(t *testing.T) {
	document := `{
	  "auto_approver_model": "work/gpt-6.1-sol",
	  "slack": {"channels": [{"channel": "#ops", "agents": ["main"], "allowed_user_ids": ["U123"]}], "feedback_channel": "C0FEEDBACK"},
	  "logging": {"level": "warn"},
	  "overlays": ["github.com/rocketable/overlay1@main"]
	}`

	var behavior Behavior
	require.NoError(t, json.Unmarshal([]byte(document), &behavior))

	var file Config
	require.NoError(t, json.Unmarshal([]byte(document), &file))
	require.Equal(t, behavior, file.Behavior(), "the document has the file's shape")

	cfg := validConfig()
	cfg.SetBehavior(&behavior)

	require.Equal(t, behavior, cfg.Behavior())
	data, err := json.Marshal(cfg.Behavior())
	require.NoError(t, err)
	require.JSONEq(t, document, string(data))
	assert.Equal(t, "xoxb-test", cfg.Slack.BotToken)
	assert.Equal(t, "xapp-test", cfg.Slack.AppToken)
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
				body := fmt.Sprintf(`{"workspace":"work","database_url":"postgres://localhost/test","openai":{"api_key":"test"},"slack":{"bot_token":"test","app_token":"test","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},"web":{"tsnet":{}},"attachments":%s}`, test.raw)
				require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

				cfg, err := Load(path, "", &AWSFetcher{})
				require.NoError(t, err)

				err = cfg.Validate()
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

func TestLoadTsnet(t *testing.T) {
	for _, tt := range []struct {
		name, web string
		want      *TsnetConfig
		wantErr   bool
	}{
		{name: "missing", web: `{}`, wantErr: true},
		{name: "default hostname", web: `{"tsnet":{}}`, want: &TsnetConfig{Hostname: "rocketclaw"}},
		{name: "configured", web: `{"tsnet":{"hostname":"phone-1","tags":["tag:rocketclaw"],"origin":"https://evil.example"}}`, want: &TsnetConfig{Hostname: "phone-1", Tags: []string{"tag:rocketclaw"}}},
		{name: "path hostname", web: `{"tsnet":{"hostname":"../state"}}`, wantErr: true},
		{name: "untagged tag", web: `{"tsnet":{"tags":["rocketclaw"]}}`, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rocketclaw.json")
			content := `{"database_url":"postgres://localhost/test","openai":{"api_key":"test"},"slack":{"bot_token":"test","app_token":"test","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},"web":` + tt.web + `}`
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

			cfg, err := Load(path, "", &AWSFetcher{})
			require.NoError(t, err)

			err = cfg.Validate()
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
	  "auto_approver_model": " openai/gpt-6.1-sol ",
	  "agent_editor_model": "work/gpt-6.1-terra#high",
	  "title_model": "work/gpt-6-luna#minimal",
	  "openai": {"api_key": "test-key"},
	  "slack": {"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},
	  "mcp_external": {"enabled": true, "listen_addr": "127.0.0.1:8765"}
	}`)

	behavior := cfg.Behavior()
	assert.Equal(t, " openai/gpt-6.1-sol ", behavior.AutoApproverModel)
	require.NoError(t, cfg.ValidateBehavior())
	assert.Equal(t, "gpt-6.1-sol", cfg.AutoApproverModel)
	assert.Equal(t, "work/gpt-6.1-terra#high", cfg.AgentEditorModel)
	assert.Equal(t, "work/gpt-6-luna#minimal", cfg.TitleModel)
}

func TestLoadPreservesNamedProviders(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "openai": {"api_key": "default-key"},
	  "providers": {
	    "work": {"api_key": "work-key", "api_base_url": "https://work.example/v1"},
	    "chat": {"auth": " chatgpt "}
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

func TestLoadPreservesDecisionConfig(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "openai": {"api_key": "test-key"},
	  "decision": {
	    "openai": {"api_key": "decide-key"},
	    "typesafe": {"api_key": "ts-key", "api_base_url": "https://ts.example"}
	  },
	  "auto_approver_decision": {"model": " typesafe/jev-latest ", "min_confidence": 0.85},
	  "slack": {"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]}
	}`)

	assert.Equal(t, map[DecisionProvider]DecisionProviderConfig{DecisionOpenAI: {APIKey: "decide-key"}, DecisionTypeSafe: {APIKey: "ts-key", APIBaseURL: "https://ts.example"}}, cfg.Decision)
	assert.Equal(t, AutoApproverDecisionConfig{Model: "typesafe/jev-latest", MinConfidence: 0.85}, cfg.AutoApproverDecision)
}

func TestValidateDecisionConfig(t *testing.T) {
	for _, tt := range []struct {
		name       string
		decision   map[DecisionProvider]DecisionProviderConfig
		model      string
		confidence float64
		wantErr    string
	}{
		{name: "none"},
		{name: "both providers", decision: map[DecisionProvider]DecisionProviderConfig{"openai": {APIKey: "a"}, "typesafe": {APIKey: "b"}}, model: "openai/gpt-6-luna", confidence: 1},
		{name: "unknown provider", decision: map[DecisionProvider]DecisionProviderConfig{"anthropic": {APIKey: "a"}}, wantErr: `decision["anthropic"]: provider must be openai or typesafe`},
		{name: "missing key", decision: map[DecisionProvider]DecisionProviderConfig{"typesafe": {}}, wantErr: `decision["typesafe"].api_key is required`},
		{name: "unconfigured model provider", decision: map[DecisionProvider]DecisionProviderConfig{"openai": {APIKey: "a"}}, model: "typesafe/jev-latest", wantErr: `auto_approver_decision.model: "typesafe/jev-latest" must be provider/model with its provider configured in decision`},
		{name: "unqualified model", decision: map[DecisionProvider]DecisionProviderConfig{"openai": {APIKey: "a"}}, model: "gpt-6-luna", wantErr: `auto_approver_decision.model: "gpt-6-luna" must be provider/model with its provider configured in decision`},
		{name: "confidence above one", confidence: 1.5, wantErr: "auto_approver_decision.min_confidence must be between 0 and 1"},
		{name: "negative confidence", confidence: -0.1, wantErr: "auto_approver_decision.min_confidence must be between 0 and 1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Decision, cfg.AutoApproverDecision = tt.decision, AutoApproverDecisionConfig{Model: tt.model, MinConfidence: tt.confidence}

			err := cfg.Validate()
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestValidatePostHog(t *testing.T) {
	cfg := validConfig()
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
			},
			want: map[string]MCPServerConfig{
				"git-hub":             {Command: "npx"},
				"sequentialthinking":  {Command: "npx"},
				"Sequential-Thinking": {URL: "https://example.com/mcp"},
			},
		},
		{
			name: "dotted name",
			servers: map[string]MCPServerConfig{
				"my_server.v2": {Command: "npx"},
			},
			wantErr: `mcp_servers["my_server.v2"]: name must not contain "."`,
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
			name: "overlapping facets",
			servers: map[string]MCPServerConfig{
				"reports": {Command: "npx", Tools: map[string][]string{"read": {"list_*", "get_report", "-get_secret_*"}, "write": {"list_x", "update_*"}, "sudo": {"*"}, "one-off_2": {"report/v?"}}},
			},
			want: map[string]MCPServerConfig{
				"reports": {Command: "npx", Tools: map[string][]string{"read": {"list_*", "get_report", "-get_secret_*"}, "write": {"list_x", "update_*"}, "sudo": {"*"}, "one-off_2": {"report/v?"}}},
			},
		},
		{
			name: "facet name with a space",
			servers: map[string]MCPServerConfig{
				"reports": {Command: "npx", Tools: map[string][]string{"read only": {"list_*"}}},
			},
			wantErr: `mcp_servers["reports"]: tools["read only"]: facet names use only letters, digits, "_" and "-"`,
		},
		{
			name: "dotted facet name",
			servers: map[string]MCPServerConfig{
				"reports": {Command: "npx", Tools: map[string][]string{"read.all": {"list_*"}}},
			},
			wantErr: `mcp_servers["reports"]: tools["read.all"]: facet names use only letters, digits, "_" and "-"`,
		},
		{
			name: "pattern with interpolation",
			servers: map[string]MCPServerConfig{
				"reports": {Command: "npx", Tools: map[string][]string{"read": {"list_*", "-${ROCKETCLAW_PRINCIPAL_ID}"}}},
			},
			wantErr: `mcp_servers["reports"]: tools["read"]: pattern "-${ROCKETCLAW_PRINCIPAL_ID}" may use only tool-name characters, "*" and "?"`,
		},
		{
			name: "pattern with a space",
			servers: map[string]MCPServerConfig{
				"reports": {Command: "npx", Tools: map[string][]string{"read": {"list reports"}}},
			},
			wantErr: `mcp_servers["reports"]: tools["read"]: pattern "list reports" may use only tool-name characters, "*" and "?"`,
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

func TestValidateRocketClaws(t *testing.T) {
	for _, tt := range []struct {
		name, key, url string
		wantErr        bool
	}{
		{name: "valid", key: "work-2", url: "https://rocketclaw.tail1234.ts.net"},
		{name: "valid port", key: "work", url: "https://rocketclaw.tail1234.ts.net:8443"},
		{name: "uppercase name", key: "Work", url: "https://rocketclaw.tail1234.ts.net", wantErr: true},
		{name: "slash name", key: "a/b", url: "https://rocketclaw.tail1234.ts.net", wantErr: true},
		{name: "http", key: "work", url: "http://rocketclaw.tail1234.ts.net", wantErr: true},
		{name: "path", key: "work", url: "https://rocketclaw.tail1234.ts.net/", wantErr: true},
		{name: "empty query", key: "work", url: "https://rocketclaw.tail1234.ts.net?", wantErr: true},
		{name: "empty fragment", key: "work", url: "https://rocketclaw.tail1234.ts.net#", wantErr: true},
		{name: "no host", key: "work", url: "https://", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.RocketClaws = map[string]RemoteRocketClawConfig{tt.key: {URL: tt.url}}

			err := cfg.Validate()
			if tt.wantErr {
				require.ErrorContains(t, err, fmt.Sprintf("rocketclaws[%q]", tt.key))
			} else {
				require.NoError(t, err)
			}

			cfg = validConfig()
			cfg.TrustedRocketClaws = []string{tt.url}

			err = cfg.Validate()
			if tt.wantErr && tt.key == "work" {
				require.ErrorContains(t, err, "trusted_rocketclaws[0]")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestPermittedSpaces(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "openai": {"api_key": "test-key"},
	  "permissions": {"Alice@Example.com": ["Ops", "Support"], "bob@example.com": ["Ops"]},
	  "slack": {"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]}
	}`)

	assert.Equal(t, []string{"Ops", "Support"}, cfg.PermittedSpaces("alice@example.com"))
	assert.Equal(t, []string{"Ops"}, cfg.PermittedSpaces("BOB@example.com"))
	assert.Nil(t, cfg.PermittedSpaces("carol@example.com"))
	assert.Nil(t, cfg.PermittedSpaces(""))

	for _, tt := range []struct {
		name        string
		permissions map[string][]string
		wantErr     string
	}{
		{name: "blank login", permissions: map[string][]string{"": {"Ops"}}, wantErr: `permissions[""]: login must not be blank or start or end with spaces`},
		{name: "padded login", permissions: map[string][]string{" alice": {"Ops"}}, wantErr: `permissions[" alice"]: login must not be blank or start or end with spaces`},
		{name: "same login ignoring case", permissions: map[string][]string{"alice": {"Ops"}, "ALICE": {"Main"}}, wantErr: `permissions["alice"]: another entry has that login, ignoring case`},
		{name: "no spaces", permissions: map[string][]string{"alice": {}}, wantErr: `permissions["alice"]: list at least one space`},
		{name: "blank space", permissions: map[string][]string{"alice": {"Ops", " "}}, wantErr: `permissions["alice"]: space names must not be blank or start or end with spaces`},
		{name: "padded space", permissions: map[string][]string{"alice": {" Ops"}}, wantErr: `permissions["alice"]: space names must not be blank or start or end with spaces`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Permissions = tt.permissions
			require.EqualError(t, cfg.Validate(), tt.wantErr)
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

func TestLoadMCPServerFacets(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "openai": {"api_key": "test-key"},
	  "slack": {"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},
	  "mcp_servers": {"gog": {"command": "gog", "tools": {"read": ["drive_search", "docs_get"], "write": ["docs_write"]}}}
	}`)

	assert.Equal(t, map[string]map[string][]string{"gog": {"read": {"drive_search", "docs_get"}, "write": {"docs_write"}}}, cfg.MCPFacets())
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
	  "web": {"tsnet": {}},
	  "mcp_external": {"enabled": true, "listen_addr": "127.0.0.1:8765"},
	  "opencode": {"box": {"url": "https://box.example:4096", "password": "pw", "port": 4100}}
	}`), 0o600))

	cfg, err := Load(path, "", &AWSFetcher{})
	require.NoError(t, err)
	require.NoError(t, cfg.Validate(), "a config written for relay ports still loads")
	assert.Equal(t, dir, cfg.Workspace)
}

func TestLoadRejectsUnreadableOrInvalidConfig(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.json"), "", &AWSFetcher{})
	require.ErrorContains(t, err, "read config")

	path := filepath.Join(t.TempDir(), "rocketclaw.json")
	require.NoError(t, os.WriteFile(path, []byte(`{`), 0o600))

	_, err = Load(path, "", &AWSFetcher{})
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

func TestLoadExternalMCPUsers(t *testing.T) {
	for _, tt := range []struct {
		name, users string
		want        map[string]string
	}{
		{name: "absent"},
		{name: "empty", users: `"users":{}`, want: map[string]string{}},
		{name: "aws reference", users: `"users":{"admin":{"aws":{"arn":"` + otherARN + `","key":"admin"}}}`, want: map[string]string{"admin": "secret"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := loadSecretConfig(t, `{"workspace":".","openai":{"api_key":"test-key"},"mcp_external":{`+tt.users+`}}`, "", mapSecrets{otherARN: `{"admin":"secret"}`})
			assert.Equal(t, tt.want, cfg.MCPExternal.Users)
		})
	}
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

	if !strings.Contains(content, `"tsnet"`) {
		content = strings.Replace(content, "{", `{"web":{"tsnet":{}},`, 1)
	}

	path := filepath.Join(t.TempDir(), "rocketclaw.json")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644), "write config")
	cfg, err := Load(path, "", &AWSFetcher{})
	require.NoError(t, err)
	require.NoError(t, cfg.Validate())

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
	cfg.Web.Tsnet = &TsnetConfig{}

	return cfg
}
