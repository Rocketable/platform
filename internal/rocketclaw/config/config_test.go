package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadThreadAgentsObjectCanDisablePreSeed(t *testing.T) {
	path := writeThreadAgentsConfig(t, `"thread_agents":{":thread:":{"agent":"main","pre_seed":false}}`)

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, ThreadAgents{":thread:": {Agent: "main", PreSeed: false}}, cfg.ThreadAgents)
}

func TestLoadDefaultsThreadAgents(t *testing.T) {
	path := writeThreadAgentsConfig(t, `"thread_agents":{}`)

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, ThreadAgents{":thread:": {Agent: "main", PreSeed: false}, ":twisted_rightward_arrows:": {Agent: "main", PreSeed: true}}, cfg.ThreadAgents)
}

func TestLoadRejectsDuplicateThreadAgentPrefixes(t *testing.T) {
	path := writeThreadAgentsConfig(t, `"thread_agents":{" :thread:":{"agent":"main"},":thread: ":{"agent":"main"}}`)

	_, err := Load(path)
	require.ErrorContains(t, err, "duplicates normalized prefix")
}

func TestNormalizeThreadAgentsDropsBlankEntries(t *testing.T) {
	agents, err := normalizeThreadAgents(ThreadAgents{" ": {Agent: "main"}, ":skip:": {Agent: " \t "}})
	require.NoError(t, err)
	assert.Empty(t, agents)
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "openai": {"api_key": "test-key"},
	  "slack": {
	    "enabled": true,
	    "bot_token": "xoxb-test",
	    "app_token": "xapp-test",
	    "room": "D123",
	    "human_user_id": "U123"
	  }
	}`)

	assert.Equal(t, "api_key", cfg.OpenAI.RocketCodeAuth)
	assert.Empty(t, cfg.AutoApproverModel)
	assert.Empty(t, cfg.SeedCompactionModel)
	assert.True(t, filepath.IsAbs(cfg.Workspace))
}

func TestLoadPreservesModelConfig(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "models": {"coding-high": "software-development-sol", "review-fast": "gpt-5.6-luna"},
	  "auto_approver_model": " openai/gpt-5.5 ",
	  "seed_compaction_model": " gpt-5.4 ",
	  "openai": {"api_key": "test-key"},
	  "mcp_external": {"enabled": true, "listen_addr": "127.0.0.1:8765"}
	}`)

	assert.Equal(t, "gpt-5.5", cfg.AutoApproverModel)
	assert.Equal(t, "gpt-5.4", cfg.SeedCompactionModel)
	assert.Equal(t, map[string]string{"coding-high": "software-development-sol", "review-fast": "gpt-5.6-luna"}, cfg.Models)
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

func TestLoadPreservesInstrumentationConfig(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "openai": {"api_key": "test-key"},
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
	  "mcp_external": {"enabled": true, "listen_addr": "127.0.0.1:8765"}
	}`)

	assert.Equal(t, []string{"github.com/rocketable/overlay1@main", "github.com/rocketable/overlay2"}, cfg.Overlays)
}

func TestLoadDefaultsWorkspaceToConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rocketclaw.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
	  "openai": {"api_key": "test-key"},
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
		{name: "no connectors", update: func(c *Config) { c.Slack.Enabled = false }, wantErr: "enable at least one connector or mcp_external"},
		{name: "workspace", update: func(c *Config) { c.Workspace = "" }, wantErr: "workspace is required"},
		{name: "rocketcode auth", update: func(c *Config) { c.OpenAI.RocketCodeAuth = "browser" }, wantErr: "openai.rocketcode_auth must be api_key or chatgpt"},
		{name: "auto approver model", update: func(c *Config) { c.AutoApproverModel = "anthropic/claude" }, wantErr: `auto_approver_model: invalid model "anthropic/claude": expected unprefixed OpenAI model ID`},
		{name: "seed compaction model", update: func(c *Config) { c.SeedCompactionModel = "openai/" }, wantErr: `seed_compaction_model: invalid model "openai/": expected openai/model`},
		{name: "mcp external listen addr", update: func(c *Config) { c.MCPExternal.Enabled = true }, wantErr: "mcp_external.listen_addr is required when mcp_external is enabled"},
		{name: "slack bot token", update: func(c *Config) { c.Slack.BotToken = "" }, wantErr: "slack.bot_token is required when slack is enabled"},
		{name: "slack app token", update: func(c *Config) { c.Slack.AppToken = "" }, wantErr: "slack.app_token is required when slack is enabled"},
		{name: "slack room", update: func(c *Config) { c.Slack.Room = "" }, wantErr: "slack.room is required when slack is enabled"},
		{name: "slack human user id", update: func(c *Config) { c.Slack.HumanUserID = "" }, wantErr: "slack.human_user_id is required when slack is enabled"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.update(cfg)

			err := cfg.Validate()
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestValidateNormalizesEmergencySafeWords(t *testing.T) {
	cfg := validConfig()
	cfg.EmergencySafeWords = []string{"  Red Button! ", "red-button", "Angstrom 42", "!!!", ""}

	require.NoError(t, cfg.Validate())
	assert.Equal(t, []string{"redbutton", "angstrom42"}, cfg.EmergencySafeWords)
}

func TestValidateSlackSocialMode(t *testing.T) {
	cfg := validConfig()
	cfg.Slack.SocialMode.Enabled = true
	cfg.Slack.SocialMode.Channels = []TextSocialChannelConfig{
		{Channel: " triage ", Agents: []string{" planner ", "", "planner", "helper"}, AllowedUserIDs: []string{" U999 ", "", "U999"}},
		{Channel: " #team ", Agents: []string{" team "}, AllowedUserIDs: []string{" U123 ", "", "U123", "U456"}},
		{Channel: " ", Agents: []string{"ignored"}},
	}

	require.NoError(t, cfg.Validate())
	assert.Equal(t, TextSocialConfig{
		Enabled: true,
		Channels: []TextSocialChannelConfig{
			{Channel: "#triage", Agents: []string{"planner", "helper"}, AllowedUserIDs: []string{"U999"}},
			{Channel: "#team", Agents: []string{"team"}, AllowedUserIDs: []string{"U123", "U456"}},
		},
		ContextMessages: 10,
	}, cfg.Slack.SocialMode)
}

func TestValidateSlackSocialModeRejectsInvalidConfig(t *testing.T) {
	for _, tt := range []struct {
		name    string
		update  func(*TextSocialConfig)
		wantErr string
	}{
		{name: "missing agents", update: func(s *TextSocialConfig) {
			s.Channels = []TextSocialChannelConfig{{Channel: "#triage", AllowedUserIDs: []string{"U123"}}}
		}, wantErr: "slack.social_mode.channels[].agents is required when slack social mode is enabled"},
		{name: "missing channel allowlist", update: func(s *TextSocialConfig) {
			s.Channels = []TextSocialChannelConfig{{Channel: "#triage", Agents: []string{"triage"}}}
		}, wantErr: "slack.social_mode.channels[].allowed_user_ids is required when slack social mode is enabled"},
		{name: "negative context", update: func(s *TextSocialConfig) { s.ContextMessages = -1 }, wantErr: "slack.social_mode.context_messages must be zero or greater"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Slack.SocialMode = TextSocialConfig{Enabled: true, Channels: []TextSocialChannelConfig{{Channel: "#triage", Agents: []string{"triage"}, AllowedUserIDs: []string{"U123"}}}, ContextMessages: 10}
			tt.update(&cfg.Slack.SocialMode)

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
	cfg.Slack.Enabled = true
	cfg.Slack.BotToken = "xoxb-test"
	cfg.Slack.AppToken = "xapp-test"
	cfg.Slack.Room = "D123"
	cfg.Slack.HumanUserID = "U123"
	cfg.OpenAI.APIKey = "test-key"

	return cfg
}

func writeThreadAgentsConfig(t *testing.T, threadAgents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rocketclaw.json")
	data := `{"workspace":".",` + threadAgents + `,"slack":{"enabled":true,"bot_token":"xoxb","app_token":"xapp","room":"D123","human_user_id":"U123"},"openai":{"api_key":"sk"}}`
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))

	return path
}
