package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadMigratesOldThreadAgents(t *testing.T) {
	path := writeThreadAgentsConfig(t, `"thread_agents":{" :thread: ":" main "}`)

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, ThreadAgents{":thread:": {Agent: "main", PreSeed: false}}, cfg.ThreadAgents)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"pre_seed": false`)
}

func TestMigrateThreadAgentsConfigLeavesCurrentShapesUntouched(t *testing.T) {
	for _, tt := range []struct {
		name string
		data string
	}{
		{name: "invalid JSON", data: `{`},
		{name: "missing thread agents", data: `{"workspace":"."}`},
		{name: "current object shape", data: `{"thread_agents":{":thread:":{"agent":"main","pre_seed":true}}}`},
		{name: "empty legacy map", data: `{"thread_agents":{}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rocketclaw.json")
			require.NoError(t, os.WriteFile(path, []byte(tt.data), 0o600))

			got, err := migrateThreadAgentsConfig(path, []byte(tt.data))
			require.NoError(t, err)
			assert.Equal(t, tt.data, string(got))
		})
	}
}

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
	  "openai": {
	    "api_key": "test-key"
	  },
	  "slack": {
	    "enabled": true,
	    "bot_token": "xoxb-test",
	    "app_token": "xapp-test",
	    "room": "D123",
	    "human_user_id": "U123"
	  }
	}`)

	assert.Equal(t, "whisper-1", cfg.OpenAI.STTModel)
	assert.Equal(t, "tts-1", cfg.OpenAI.TTSModel)
	assert.Equal(t, "alloy", cfg.OpenAI.TTSVoice)
	assert.Equal(t, "test-key", cfg.OpenAI.STTAPIKey)
	assert.Equal(t, "test-key", cfg.OpenAI.TTSAPIKey)
	assert.False(t, cfg.WebUI.Enabled)
	assert.Empty(t, cfg.WebUI.ListenAddr)
	assert.Equal(t, "api_key", cfg.OpenAI.RocketCodeAuth)
	assert.True(t, filepath.IsAbs(cfg.Workspace))
	assert.Zero(t, cfg.MinimumWaitAfterHumanInteractionDuration)
}

func TestLoadPreservesAnthropicConfig(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "openai": {
	    "api_key": "test-key"
	  },
	  "anthropic": {
	    "api_key": "anthropic-key",
	    "api_base_url": "https://anthropic.example/v1"
	  },
	  "web_ui": {
	    "enabled": true,
	    "listen_addr": "127.0.0.1:8766"
	  }
	}`)

	assert.Equal(t, "anthropic-key", cfg.Anthropic.APIKey)
	assert.Equal(t, "https://anthropic.example/v1", cfg.Anthropic.APIBaseURL)
}

func TestLoadNormalizesOverlays(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "overlays": [" github.com/rocketable/overlay1@main ", "", "github.com/rocketable/overlay2"],
	  "openai": {
	    "api_key": "test-key"
	  },
	  "web_ui": {
	    "enabled": true,
	    "listen_addr": "127.0.0.1:8766"
	  }
	}`)

	assert.Equal(t, []string{"github.com/rocketable/overlay1@main", "github.com/rocketable/overlay2"}, cfg.Overlays)
}

func TestLoadDefaultsWorkspaceToConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rocketclaw.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
	  "openai": {
	    "api_key": "test-key"
	  },
	  "web_ui": {
	    "enabled": true,
	    "listen_addr": "127.0.0.1:8766"
	  }
	}`), 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, dir, cfg.Workspace)
}

func TestLoadPreservesExplicitWebUIListenAddr(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "web_ui": {
	    "enabled": true,
	    "listen_addr": "0.0.0.0:9999"
	  },
	  "openai": {
	    "api_key": "test-key"
	  },
	  "slack": {
	    "enabled": true,
	    "bot_token": "xoxb-test",
	    "app_token": "xapp-test",
	    "room": "D123",
	    "human_user_id": "U123"
	  }
	}`)

	assert.True(t, cfg.WebUI.Enabled)
	assert.Equal(t, "0.0.0.0:9999", cfg.WebUI.ListenAddr)
}

func TestLoadPreservesExplicitWebUICertificateFiles(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "web_ui": {
	    "enabled": true,
	    "listen_addr": "0.0.0.0:9999",
	    "cert_file": "web-ui.crt",
	    "key_file": "web-ui.key"
	  },
	  "openai": {
	    "api_key": "test-key"
	  }
	}`)

	assert.Equal(t, "web-ui.crt", cfg.WebUI.CertFile)
	assert.Equal(t, "web-ui.key", cfg.WebUI.KeyFile)
}

func TestLoadLeavesWebUIDisabledWhenExplicitlyFalse(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "web_ui": {
	    "enabled": false
	  },
	  "openai": {
	    "api_key": "test-key"
	  },
	  "slack": {
	    "enabled": true,
	    "bot_token": "xoxb-test",
	    "app_token": "xapp-test",
	    "room": "D123",
	    "human_user_id": "U123"
	  }
	}`)

	assert.False(t, cfg.WebUI.Enabled)
	assert.Empty(t, cfg.WebUI.ListenAddr)
}

func TestLoadFallsBackOpenAIServiceOverridesToSharedDefaults(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "openai": {
	    "api_key": "shared-key",
	    "api_base_url": "https://example.com/v1",
	    "stt_key": "   ",
	    "stt_base_url": "",
	    "tts_key": "",
	    "tts_base_url": "   "
	  },
	  "slack": {
	    "enabled": true,
	    "bot_token": "xoxb-test",
	    "app_token": "xapp-test",
	    "room": "D123",
	    "human_user_id": "U123"
	  }
	}`)

	assert.Equal(t, "shared-key", cfg.OpenAI.STTAPIKey)
	assert.Equal(t, "https://example.com/v1", cfg.OpenAI.STTAPIBaseURL)
	assert.Equal(t, "shared-key", cfg.OpenAI.TTSAPIKey)
	assert.Equal(t, "https://example.com/v1", cfg.OpenAI.TTSAPIBaseURL)
}

func TestLoadRejectsUnreadableOrInvalidConfig(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	require.ErrorContains(t, err, "read config")

	path := filepath.Join(t.TempDir(), "rocketclaw.json")
	require.NoError(t, os.WriteFile(path, []byte(`{`), 0o600))

	_, err = Load(path)
	require.ErrorContains(t, err, "parse config JSON")
}

func TestValidatePreservesExplicitOpenAIServiceOverrides(t *testing.T) {
	cfg := validConfig()
	cfg.DiscordVoice.Enabled = false
	cfg.Slack.Enabled = true
	cfg.Slack.BotToken = "xoxb-test"
	cfg.Slack.AppToken = "xapp-test"
	cfg.Slack.Room = "D123"
	cfg.Slack.HumanUserID = "U123"
	cfg.OpenAI.APIBaseURL = "https://shared.example/v1"
	cfg.OpenAI.STTAPIKey = "stt-key"
	cfg.OpenAI.STTAPIBaseURL = "https://stt.example/v1"
	cfg.OpenAI.TTSAPIKey = "tts-key"
	cfg.OpenAI.TTSAPIBaseURL = "https://tts.example/v1"

	require.NoError(t, cfg.Validate())
	assert.Equal(t, "stt-key", cfg.OpenAI.STTAPIKey)
	assert.Equal(t, "https://stt.example/v1", cfg.OpenAI.STTAPIBaseURL)
	assert.Equal(t, "tts-key", cfg.OpenAI.TTSAPIKey)
	assert.Equal(t, "https://tts.example/v1", cfg.OpenAI.TTSAPIBaseURL)
}

func TestValidateRejectsMissingRequiredConfig(t *testing.T) {
	for _, tt := range []struct {
		name    string
		update  func(*Config)
		wantErr string
	}{
		{
			name:    "no connectors",
			update:  func(c *Config) { c.DiscordVoice.Enabled = false },
			wantErr: "enable at least one connector, web_ui, or mcp_external",
		},
		{
			name:    "workspace",
			update:  func(c *Config) { c.Workspace = "" },
			wantErr: "workspace is required",
		},
		{
			name:    "rocketcode auth",
			update:  func(c *Config) { c.OpenAI.RocketCodeAuth = "browser" },
			wantErr: "openai.rocketcode_auth must be api_key or chatgpt",
		},
		{
			name:    "openai api key",
			update:  func(c *Config) { c.OpenAI.APIKey = "" },
			wantErr: "openai.api_key is required",
		},
		{
			name:    "discord token",
			update:  func(c *Config) { c.DiscordVoice.Token = "" },
			wantErr: "discord_voice.token is required when discord_voice is enabled",
		},
		{
			name: "discord text token",
			update: func(c *Config) {
				c.DiscordVoice.Enabled = false
				c.DiscordText = DiscordTextConfig{Enabled: true, ChannelID: "channel-123", HumanUserID: "user-123"}
			},
			wantErr: "discord_text.token is required when discord_text is enabled",
		},
		{
			name: "discord text channel",
			update: func(c *Config) {
				c.DiscordVoice.Enabled = false
				c.DiscordText = DiscordTextConfig{Enabled: true, Token: "discord-token", HumanUserID: "user-123"}
			},
			wantErr: "discord_text.channel_id is required when discord_text is enabled",
		},
		{
			name: "discord text human user",
			update: func(c *Config) {
				c.DiscordVoice.Enabled = false
				c.DiscordText = DiscordTextConfig{Enabled: true, Token: "discord-token", ChannelID: "channel-123"}
			},
			wantErr: "discord_text.human_user_id is required when discord_text is enabled",
		},
		{
			name: "slack and discord text",
			update: func(c *Config) {
				c.DiscordText = DiscordTextConfig{Enabled: true, Token: "discord-token", ChannelID: "channel-123", HumanUserID: "user-123"}
				c.Slack.Enabled = true
			},
			wantErr: "slack and discord_text are mutually exclusive primary text connectors",
		},
		{
			name:    "mcp external listen addr",
			update:  func(c *Config) { c.MCPExternal.Enabled = true },
			wantErr: "mcp_external.listen_addr is required when mcp_external is enabled",
		},
		{
			name:    "slack bot token",
			update:  func(c *Config) { c.Slack.Enabled, c.Slack.BotToken = true, "" },
			wantErr: "slack.bot_token is required when slack is enabled",
		},
		{
			name:    "slack app token",
			update:  func(c *Config) { c.Slack.Enabled, c.Slack.AppToken = true, "" },
			wantErr: "slack.app_token is required when slack is enabled",
		},
		{
			name:    "slack room",
			update:  func(c *Config) { c.Slack.Enabled, c.Slack.Room = true, "" },
			wantErr: "slack.room is required when slack is enabled",
		},
		{
			name:    "slack human user id",
			update:  func(c *Config) { c.Slack.Enabled, c.Slack.HumanUserID = true, "" },
			wantErr: "slack.human_user_id is required when slack is enabled",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Slack.BotToken = "xoxb-test"
			cfg.Slack.AppToken = "xapp-test"
			cfg.Slack.Room = "D123"
			cfg.Slack.HumanUserID = "U123"
			tt.update(cfg)

			err := cfg.Validate()
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestValidateNormalizesEmergencySafeWords(t *testing.T) {
	cfg := validConfig()
	cfg.EmergencySafeWords = []string{"  Red Button! ", "red-button", "Ångström 42", "!!!", ""}

	require.NoError(t, cfg.Validate())
	assert.Equal(t, []string{"redbutton", "ångström42"}, cfg.EmergencySafeWords)
}

func TestValidateNormalizesExternalMCPAllowedAgents(t *testing.T) {
	cfg := validConfig()
	cfg.MCPExternal.AllowedAgents = []string{" main ", "", "main", "worker"}

	require.NoError(t, cfg.Validate())
	assert.Equal(t, []string{"main", "worker"}, cfg.MCPExternal.AllowedAgents)
}

func TestValidateSlackSocialMode(t *testing.T) {
	cfg := validConfig()
	cfg.Slack.Enabled = true
	cfg.Slack.BotToken = "xoxb-test"
	cfg.Slack.AppToken = "xapp-test"
	cfg.Slack.Room = "D123"
	cfg.Slack.HumanUserID = "U123"
	cfg.Slack.SocialMode.Enabled = true
	cfg.Slack.SocialMode.ChannelAgents = map[string]string{"#triage": "planner"}
	cfg.Slack.SocialMode.AllowedUserIDs = []string{" U123 ", "", "U123", "U456"}

	require.NoError(t, cfg.Validate())
	assert.Equal(t, SlackSocialConfig{Enabled: true, ChannelAgents: map[string]string{"#triage": "planner"}, AllowedUserIDs: []string{"U123", "U456"}, ContextMessages: 10}, cfg.Slack.SocialMode)
}

func TestLoadIgnoresStaleSlackSocialModeAgent(t *testing.T) {
	cfg := loadTestConfig(t, `{
	  "workspace": ".",
	  "discord_voice": {
	    "enabled": true,
	    "token": "discord-token",
	    "voice_channel_id": "voice-123",
	    "human_user_id": "user-123"
	  },
	  "openai": {
	    "api_key": "test-key"
	  },
	  "slack": {
	    "enabled": true,
	    "bot_token": "xoxb-test",
	    "app_token": "xapp-test",
	    "room": "D123",
	    "human_user_id": "U123",
	    "social_mode": {
	      "enabled": true,
	      "agent": "stale",
	      "channel_agents": {},
	      "allowed_user_ids": ["U123"]
	    }
	  }
	}`)

	assert.Empty(t, cfg.Slack.SocialMode.ChannelAgents)
}

func TestValidateSlackSocialModeRejectsInvalidConfig(t *testing.T) {
	for _, tt := range []struct {
		name    string
		update  func(*SlackSocialConfig)
		wantErr string
	}{
		{name: "missing allowlist", update: func(s *SlackSocialConfig) { s.AllowedUserIDs = nil }, wantErr: "slack.social_mode.allowed_user_ids is required when slack social mode is enabled"},
		{name: "negative context", update: func(s *SlackSocialConfig) { s.ContextMessages = -1 }, wantErr: "slack.social_mode.context_messages must be zero or greater"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Slack.Enabled = true
			cfg.Slack.BotToken = "xoxb-test"
			cfg.Slack.AppToken = "xapp-test"
			cfg.Slack.Room = "D123"
			cfg.Slack.HumanUserID = "U123"
			cfg.Slack.SocialMode = SlackSocialConfig{Enabled: true, AllowedUserIDs: []string{"U123"}, ContextMessages: 10}
			tt.update(&cfg.Slack.SocialMode)

			err := cfg.Validate()
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestValidateMinimumWaitAfterHumanInteraction(t *testing.T) {
	for _, tt := range []struct {
		name    string
		raw     string
		want    time.Duration
		wantErr string
	}{
		{name: "blank", raw: " \t\n ", want: 0},
		{name: "valid duration", raw: " 250ms ", want: 250 * time.Millisecond},
		{name: "invalid duration", raw: "soon", wantErr: "parse minimum_wait_after_human_interaction"},
		{name: "negative duration", raw: "-1s", wantErr: "minimum_wait_after_human_interaction must be zero or greater"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.MinimumWaitAfterHumanInteraction = tt.raw

			err := cfg.Validate()
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.MinimumWaitAfterHumanInteractionDuration)
		})
	}
}

func TestValidateRequiresWebUIListenAddrWhenEnabled(t *testing.T) {
	cfg := validConfig()
	cfg.WebUI.Enabled = true

	err := cfg.Validate()
	require.ErrorContains(t, err, "web_ui.listen_addr is required when web_ui is enabled")
}

func TestValidateRejectsIPv6WebUIListenAddr(t *testing.T) {
	cfg := validConfig()
	cfg.WebUI.Enabled = true
	cfg.WebUI.ListenAddr = "[::1]:8766"

	err := cfg.Validate()
	require.ErrorContains(t, err, "web_ui.listen_addr must be IPv4-only")
}

func TestValidateRejectsMalformedWebUIListenAddr(t *testing.T) {
	cfg := validConfig()
	cfg.WebUI.Enabled = true
	cfg.WebUI.ListenAddr = "127.0.0.1"

	err := cfg.Validate()
	require.ErrorContains(t, err, "parse web_ui.listen_addr")
}

func TestValidateRequiresWebUICertAndKeyTogether(t *testing.T) {
	cfg := validConfig()
	cfg.WebUI.Enabled = true
	cfg.WebUI.ListenAddr = DefaultWebUIListenAddr
	cfg.WebUI.CertFile = "web-ui.crt"

	err := cfg.Validate()
	require.ErrorContains(t, err, "web_ui.cert_file and web_ui.key_file must be set together")
}

func TestValidateAllowsWebUIOnly(t *testing.T) {
	cfg := new(Config)
	cfg.Workspace = "/tmp/project"
	cfg.WebUI.Enabled = true
	cfg.WebUI.ListenAddr = DefaultWebUIListenAddr
	cfg.OpenAI.APIKey = "test-key"

	require.NoError(t, cfg.Validate())
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
	cfg.DiscordVoice.Enabled = true
	cfg.DiscordVoice.Token = "discord-token"
	cfg.DiscordVoice.VoiceChannelID = "voice-123"
	cfg.DiscordVoice.HumanUserID = "user-123"
	cfg.OpenAI.APIKey = "test-key"

	return cfg
}

func writeThreadAgentsConfig(t *testing.T, threadAgents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rocketclaw.json")
	data := `{"workspace":".",` + threadAgents + `,"minimum_wait_after_human_interaction":"","slack":{"enabled":true,"bot_token":"xoxb","app_token":"xapp","room":"D123","human_user_id":"U123"},"openai":{"api_key":"sk"}}`
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))

	return path
}
