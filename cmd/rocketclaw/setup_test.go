package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/require"
)

func TestRunSetupWritesSlackConfigWithoutBufferTimeout(t *testing.T) {
	workspace, cfg := runSetupWithInput(t, slackSetupInput(""), nil)

	require.Equal(t, config.SlackConfig{Enabled: true, BotToken: "xoxb-test", AppToken: "xapp-test", Room: "D123", HumanUserID: "U123"}, cfg.Slack)
	require.Equal(t, config.OpenAIConfig{APIKey: "sk-test", APIBaseURL: "", RocketCodeAuth: "api_key", STTModel: "gpt-4o-mini-transcribe", STTPrompt: "", STTAPIKey: "", STTAPIBaseURL: "", TTSModel: "gpt-4o-mini-tts", TTSVoice: "alloy", TTSInstructions: "", TTSAPIKey: "", TTSAPIBaseURL: ""}, cfg.OpenAI)
	require.True(t, cfg.WebUI.Enabled)
	require.Equal(t, config.DefaultWebUIListenAddr, cfg.WebUI.ListenAddr)
	require.Equal(t, config.ThreadAgents{":thread:": {Agent: "main", PreSeed: false}, ":twisted_rightward_arrows:": {Agent: "main", PreSeed: true}}, cfg.ThreadAgents)

	for _, name := range []string{"AGENTS.md", "main-update-cortex.sh"} {
		data, err := os.ReadFile(filepath.Join(workspace, name))
		require.NoError(t, err)
		require.NotEmpty(t, data)
	}

	agentsData, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	require.NoError(t, err)
	require.NotContains(t, string(agentsData), "%HUMAN_PARTNER_NAME%")
	require.Contains(t, string(agentsData), "Ulderico")

	mainAgentData, err := os.ReadFile(filepath.Join(workspace, "agents", "main.md"))
	require.NoError(t, err)
	require.NotContains(t, string(mainAgentData), "%HUMAN_PARTNER_NAME%")
	require.NotContains(t, string(mainAgentData), "%AGENT_NAME%")
	require.Contains(t, string(mainAgentData), "Ulderico")
	require.Contains(t, string(mainAgentData), "Maschine")

	runtimeMainAgentData, err := os.ReadFile(filepath.Join(workspace, ".rocketclaw", "agents", "main.md"))
	require.NoError(t, err)
	require.NotContains(t, string(runtimeMainAgentData), "%HUMAN_PARTNER_NAME%")
	require.NotContains(t, string(runtimeMainAgentData), "%AGENT_NAME%")
	require.Contains(t, string(runtimeMainAgentData), "Ulderico")
	require.Contains(t, string(runtimeMainAgentData), "Maschine")
}

func TestRunSetupAllowsMCPOnlyMode(t *testing.T) {
	workspace, cfg := runSetupWithInput(t, mcpOnlySetupInput("", ""), nil)

	require.False(t, cfg.DiscordVoice.Enabled)
	require.False(t, cfg.DiscordText.Enabled)
	require.False(t, cfg.Slack.Enabled)
	require.True(t, cfg.MCPExternal.Enabled)
	require.False(t, cfg.WebUI.Enabled)
	require.Empty(t, cfg.WebUI.ListenAddr)
	require.Equal(t, "127.0.0.1:8765", cfg.MCPExternal.ListenAddr)

	_, err := os.Stat(filepath.Join(workspace, externalMCPUsersFilename))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRunSetupAllowsWebUIOnlyMode(t *testing.T) {
	_, cfg := runSetupWithInput(t, webUIOnlySetupInput(""), nil)

	require.False(t, cfg.DiscordVoice.Enabled)
	require.False(t, cfg.DiscordText.Enabled)
	require.False(t, cfg.Slack.Enabled)
	require.False(t, cfg.MCPExternal.Enabled)
	require.True(t, cfg.WebUI.Enabled)
	require.Equal(t, config.DefaultWebUIListenAddr, cfg.WebUI.ListenAddr)
}

func TestRunSetupRepromptsWhenNoConnectorSelected(t *testing.T) {
	_, cfg, output := runSetupWithInputOutput(t, strings.Join([]string{
		"n",
		"none",
		"n",
		"n",
		"n",
		"none",
		"n",
		"y",
		"sk-test",
		"",
		"",
		"",
		"",
		"",
		"Ulderico",
		"Maschine",
		"",
		"",
	}, "\n")+"\n", nil)

	require.Contains(t, output, "At least one connector, browser voice mode, or external MCP server must be enabled.")
	require.True(t, cfg.WebUI.Enabled)
	require.Equal(t, config.DefaultWebUIListenAddr, cfg.WebUI.ListenAddr)
}

func TestRunSetupWritesDiscordConfig(t *testing.T) {
	_, cfg := runSetupWithInput(t, strings.Join([]string{
		"y",
		"none",
		"n",
		"n",
		"sk-test",
		"",
		"",
		"",
		"",
		"",
		"Ulderico",
		"Maschine",
		"",
		"discord-token",
		"voice-123",
		"user-123",
	}, "\n")+"\n", nil)

	require.Equal(t, config.DiscordVoiceConfig{Enabled: true, Token: "discord-token", VoiceChannelID: "voice-123", HumanUserID: "user-123"}, cfg.DiscordVoice)
	require.False(t, cfg.WebUI.Enabled)
	require.Empty(t, cfg.WebUI.ListenAddr)
}

func TestRunSetupWritesDiscordTextConfig(t *testing.T) {
	_, cfg := runSetupWithInput(t, strings.Join([]string{
		"n",
		"discord",
		"n",
		"n",
		"sk-test",
		"",
		"",
		"",
		"",
		"",
		"Ulderico",
		"Maschine",
		"",
		"discord-token",
		"channel-123",
		"user-123",
	}, "\n")+"\n", nil)

	require.Equal(t, config.DiscordTextConfig{Enabled: true, Token: "discord-token", ChannelID: "channel-123", HumanUserID: "user-123"}, cfg.DiscordText)
	require.False(t, cfg.Slack.Enabled)
	require.False(t, cfg.WebUI.Enabled)
}

func TestRunSetupAcceptsConfiguredWebUIListenAddr(t *testing.T) {
	_, cfg := runSetupWithInput(t, webUIOnlySetupInput("127.0.0.1:8767"), nil)

	require.True(t, cfg.WebUI.Enabled)
	require.Equal(t, "127.0.0.1:8767", cfg.WebUI.ListenAddr)
}

func TestRunSetupRepromptsForInvalidWebUIEnablement(t *testing.T) {
	_, cfg, output := runSetupWithInputOutput(t, strings.Join([]string{
		"n",
		"n",
		"n",
		"maybe",
		"",
		"sk-test",
		"",
		"",
		"",
		"",
		"",
		"Ulderico",
		"Maschine",
		"",
		"",
	}, "\n")+"\n", nil)

	require.Contains(t, output, "Please answer yes or no.")
	require.True(t, cfg.WebUI.Enabled)
	require.Equal(t, config.DefaultWebUIListenAddr, cfg.WebUI.ListenAddr)
}

func TestPromptYesNoDefault(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		defaultValue bool
		want         bool
		wantOutput   string
	}{
		{name: "blank uses true default", input: "\n", defaultValue: true, want: true},
		{name: "blank uses false default", input: "\n", defaultValue: false, want: false},
		{name: "yes", input: "yes\n", want: true},
		{name: "no", input: "no\n", defaultValue: true, want: false},
		{name: "invalid then yes", input: "later\ny\n", want: true, wantOutput: "Please answer yes or no."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := bufio.NewReader(strings.NewReader(tt.input))

			var got bool

			output := captureStdout(t, func() error {
				var err error

				got, err = promptYesNoDefault(reader, "Continue? ", tt.defaultValue)

				return err
			})

			require.Equal(t, tt.want, got)
			require.Contains(t, output, "Continue? ")

			if tt.wantOutput != "" {
				require.Contains(t, output, tt.wantOutput)
			}
		})
	}
}

func TestPromptInputReportsReadError(t *testing.T) {
	_, err := promptInput(bufio.NewReader(strings.NewReader("")), "Name: ")

	require.ErrorContains(t, err, "read prompt input")
}

func TestInterviewSetupReportsPromptReadErrors(t *testing.T) {
	common := strings.Join([]string{"sk-test", "", "", "", "", "", "Ulderico", "Maschine", ""}, "\n") + "\n"

	for _, tt := range []struct {
		name    string
		input   string
		wantErr string
	}{
		{name: "discord enablement", wantErr: "prompt Discord enablement"},
		{name: "primary text connector", input: "n\n", wantErr: "prompt primary text connector"},
		{name: "external MCP enablement", input: "n\nnone\n", wantErr: "prompt external MCP enablement"},
		{name: "browser voice mode enablement", input: "n\n" + "none\n" + "n\n", wantErr: "prompt browser voice mode enablement"},
		{name: "common fields", input: "n\n" + "none\n" + "n\n\n", wantErr: "read prompt input"},
		{name: "discord fields", input: "y\n" + "none\n" + "n\n\n" + common, wantErr: "read prompt input"},
		{name: "slack fields", input: "n\n" + "slack\n" + "n\n\n" + common, wantErr: "read prompt input"},
		{name: "discord text fields", input: "n\n" + "discord\n" + "n\n\n" + common, wantErr: "read prompt input"},
		{name: "external MCP listen address", input: "n\n" + "none\n" + "y\n\n" + common, wantErr: "read prompt input"},
		{name: "external MCP users file", input: "n\n" + "none\n" + "y\n\n" + common + "\n", wantErr: "prompt external MCP users file creation"},
		{name: "browser voice mode listen address", input: "n\n" + "none\n" + "n\n\n" + common, wantErr: "read prompt input"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stdinFile, err := os.CreateTemp(t.TempDir(), "setup-input-*.txt")
			require.NoError(t, err)
			_, err = stdinFile.WriteString(tt.input)
			require.NoError(t, err)
			_, err = stdinFile.Seek(0, 0)
			require.NoError(t, err)

			oldStdin := os.Stdin
			os.Stdin = stdinFile

			t.Cleanup(func() {
				os.Stdin = oldStdin

				require.NoError(t, stdinFile.Close())
			})

			var (
				cfg      config.Config
				errSetup error
			)

			captureStdout(t, func() error {
				_, errSetup = interviewSetup(&cfg)

				return nil
			})

			require.ErrorContains(t, errSetup, tt.wantErr)
		})
	}
}

func TestRunSetupFilesReportsStdoutErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{name: "list", args: []string{"list"}, want: "print embedded setup file list"},
		{name: "get", args: []string{"get", "AGENTS.md"}, want: "print embedded setup file AGENTS.md"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			require.NoError(t, err)

			oldStdout := os.Stdout
			os.Stdout = writer

			defer func() { os.Stdout = oldStdout }()

			require.NoError(t, writer.Close())

			err = runSetupFiles(tt.args)
			require.ErrorContains(t, err, tt.want)
			require.NoError(t, reader.Close())
		})
	}
}

func TestWriteConfigReportsFileErrors(t *testing.T) {
	workspace := t.TempDir()
	parentFile := filepath.Join(workspace, "parent")
	require.NoError(t, os.WriteFile(parentFile, []byte("not a directory"), 0o600))

	err := writeConfig(filepath.Join(parentFile, "rocketclaw.json"), &config.Config{})
	require.ErrorContains(t, err, "create config parent directory")

	err = writeConfig(workspace, &config.Config{})
	require.ErrorContains(t, err, "write config file")
}

func TestReplaceFilePlaceholders(t *testing.T) {
	workspace := t.TempDir()
	replacer := strings.NewReplacer("%NAME%", "RocketClaw")

	err := replaceFilePlaceholders(filepath.Join(workspace, "missing.md"), replacer)
	require.ErrorContains(t, err, "read file")

	path := filepath.Join(workspace, "agent.md")
	require.NoError(t, os.WriteFile(path, []byte("Hello, %NAME%!"), 0o600))
	require.NoError(t, replaceFilePlaceholders(path, replacer))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "Hello, RocketClaw!", string(data))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestPromptFieldsRepromptsRequiredAndKeepsOptionalDefault(t *testing.T) {
	required := "old"
	optional := "existing"
	reader := bufio.NewReader(strings.NewReader("\n accepted \n\n"))

	output := captureStdout(t, func() error {
		return promptFields(reader,
			promptField{prompt: "Required: ", required: true, value: &required},
			promptField{prompt: "Optional: ", value: &optional},
		)
	})

	require.Equal(t, "accepted", required)
	require.Equal(t, "existing", optional)
	require.Contains(t, output, "Required: ")
	require.Contains(t, output, "This value is required.")
	require.Contains(t, output, "Optional: ")
}

func TestRunSetupAcceptsConfiguredExternalMCPListenAddr(t *testing.T) {
	_, cfg := runSetupWithInput(t, mcpOnlySetupInput("0.0.0.0:8765", "n"), nil)

	require.True(t, cfg.MCPExternal.Enabled)
	require.Equal(t, "0.0.0.0:8765", cfg.MCPExternal.ListenAddr)
}

func TestRunSetupAcceptsConfiguredOpenAIAPIBase(t *testing.T) {
	_, cfg := runSetupWithInput(t, slackSetupInput("https://example.com/v1"), nil)

	require.Equal(t, "https://example.com/v1", cfg.OpenAI.APIBaseURL)
}

func TestRunSetupAcceptsConfiguredOpenAIServiceOverrides(t *testing.T) {
	_, cfg := runSetupWithInput(t, slackSetupInputWithServiceOverrides(
		"https://shared.example/v1",
		"stt-key",
		"https://stt.example/v1",
		"tts-key",
		"https://tts.example/v1",
	), nil)

	require.Equal(t, "stt-key", cfg.OpenAI.STTAPIKey)
	require.Equal(t, "https://stt.example/v1", cfg.OpenAI.STTAPIBaseURL)
	require.Equal(t, "tts-key", cfg.OpenAI.TTSAPIKey)
	require.Equal(t, "https://tts.example/v1", cfg.OpenAI.TTSAPIBaseURL)
}

func TestRunSetupPreservesBlankOpenAIServiceOverrides(t *testing.T) {
	_, cfg := runSetupWithInput(t, slackSetupInput("https://example.com/v1"), nil)

	require.Empty(t, cfg.OpenAI.STTAPIKey)
	require.Empty(t, cfg.OpenAI.STTAPIBaseURL)
	require.Empty(t, cfg.OpenAI.TTSAPIKey)
	require.Empty(t, cfg.OpenAI.TTSAPIBaseURL)
}

func TestRunSetupCreatesExternalMCPUsersFileWhenRequested(t *testing.T) {
	workspace, cfg, output := runSetupWithInputOutput(t, mcpOnlySetupInput("", "y"), nil)

	require.True(t, cfg.MCPExternal.Enabled)

	usersPath := filepath.Join(workspace, externalMCPUsersFilename)
	info, err := os.Stat(usersPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	data, err := os.ReadFile(usersPath)
	require.NoError(t, err)

	var users map[string]string
	require.NoError(t, json.Unmarshal(data, &users))
	require.Len(t, users, 1)
	password := users["admin"]
	require.NotEmpty(t, password)
	require.Contains(t, output, "Wrote "+externalMCPUsersFilename)
	require.NotContains(t, output, password)
}

func TestRunSetupPreservesExistingRootSetupFiles(t *testing.T) {
	workspace, _ := runSetupWithInput(t, slackSetupInput(""), func(workspace string) {
		for _, name := range []string{"AGENTS.md", "main-update-cortex.sh"} {
			require.NoError(t, os.WriteFile(filepath.Join(workspace, name), []byte(name+" preserved\n"), 0o755))
		}
	})

	for _, name := range []string{"AGENTS.md", "main-update-cortex.sh"} {
		data, err := os.ReadFile(filepath.Join(workspace, name))
		require.NoError(t, err)
		require.Equal(t, name+" preserved\n", string(data))
	}
}

func TestRunSetupFilesListShowsKnownFiles(t *testing.T) {
	output := captureStdout(t, func() error {
		return runSetup([]string{"files", "list"})
	})

	require.Contains(t, output, "AGENTS.md\n")
	require.Contains(t, output, "main-update-cortex.sh\n")
	require.NotContains(t, output, "main-split-markdown-files.sh\n")
	require.Contains(t, output, "agents/main.md\n")
	require.Contains(t, output, ".rocketclaw/skills/main-create-or-update-agent/SKILL.md\n")
}

func TestRunSetupFilesGetReturnsEmbeddedContent(t *testing.T) {
	output := captureStdout(t, func() error {
		return runSetup([]string{"files", "get", "AGENTS.md"})
	})

	require.Contains(t, output, "# Behavioral Risk Management")
	require.Contains(t, output, "# Cortex")
}

func TestRunSetupFilesGetReportsUnknownFile(t *testing.T) {
	err := runSetup([]string{"files", "get", "missing.md"})
	require.ErrorContains(t, err, "read embedded setup file missing.md")
	require.ErrorContains(t, err, "unknown embedded setup file")
}

func TestRunSetupRejectsInvalidSubcommands(t *testing.T) {
	for _, tt := range []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "unknown setup subcommand", args: []string{"bogus"}, wantErr: "setup accepts only the `files` subcommand"},
		{name: "missing files action", args: []string{"files"}, wantErr: "setup files requires `list` or `get <filename>`"},
		{name: "extra list argument", args: []string{"files", "list", "extra"}, wantErr: "setup files list does not accept arguments"},
		{name: "missing get filename", args: []string{"files", "get"}, wantErr: "setup files get accepts exactly one filename"},
		{name: "unknown files action", args: []string{"files", "bogus"}, wantErr: "setup files requires `list` or `get <filename>`"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := runSetup(tt.args)
			require.EqualError(t, err, tt.wantErr)
		})
	}
}

func runSetupWithInput(t *testing.T, input string, prepare func(string)) (string, config.Config) {
	t.Helper()

	workspace, cfg, _ := runSetupWithInputOutput(t, input, prepare)

	return workspace, cfg
}

func runSetupWithInputOutput(t *testing.T, input string, prepare func(string)) (workspace string, cfg config.Config, output string) {
	t.Helper()

	workspace = t.TempDir()
	if prepare != nil {
		prepare(workspace)
	}

	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(cwd))
	})

	stdinFile, err := os.CreateTemp(t.TempDir(), "setup-input-*.txt")
	require.NoError(t, err)
	_, err = stdinFile.WriteString(input)
	require.NoError(t, err)
	_, err = stdinFile.Seek(0, 0)
	require.NoError(t, err)

	oldStdin := os.Stdin
	os.Stdin = stdinFile

	t.Cleanup(func() {
		os.Stdin = oldStdin

		require.NoError(t, stdinFile.Close())
	})

	output = captureStdout(t, func() error {
		return runSetup(nil)
	})

	configData, err := os.ReadFile(filepath.Join(workspace, defaultConfigPath))
	require.NoError(t, err)

	require.NoError(t, json.Unmarshal(configData, &cfg))

	return workspace, cfg, output
}

func slackSetupInput(apiBase string) string {
	return slackSetupInputWithServiceOverrides(apiBase, "", "", "", "")
}

func slackSetupInputWithServiceOverrides(apiBase, sttKey, sttBase, ttsKey, ttsBase string) string {
	return strings.Join([]string{
		"n",
		"slack",
		"n",
		"",
		"sk-test",
		apiBase,
		sttKey,
		sttBase,
		ttsKey,
		ttsBase,
		"Ulderico",
		"Maschine",
		"",
		"xoxb-test",
		"xapp-test",
		"D123",
		"U123",
		"",
	}, "\n") + "\n"
}

func mcpOnlySetupInput(listenAddr, createExternalMCPUsers string) string {
	return strings.Join([]string{
		"n",
		"none",
		"y",
		"n",
		"sk-test",
		"",
		"",
		"",
		"",
		"",
		"Ulderico",
		"Maschine",
		"",
		listenAddr,
		createExternalMCPUsers,
	}, "\n") + "\n"
}

func webUIOnlySetupInput(listenAddr string) string {
	return strings.Join([]string{
		"n",
		"none",
		"n",
		"y",
		"sk-test",
		"",
		"",
		"",
		"",
		"",
		"Ulderico",
		"Maschine",
		"",
		listenAddr,
	}, "\n") + "\n"
}
