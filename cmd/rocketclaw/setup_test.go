package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/require"
)

func TestRunSetupWritesSlackConfig(t *testing.T) {
	workspace, cfg, output := runSetupWithInputOutput(t, slackSetupInput(""), nil)

	require.Equal(t, config.SlackConfig{
		BotToken: "xoxb-test",
		AppToken: "xapp-test",
		Channels: []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"Maschine"}, AllowedUserIDs: []string{"U123"}}},
	}, cfg.Slack)
	require.Equal(t, config.OpenAIConfig{APIKey: "sk-test", APIBaseURL: "", RocketCodeAuth: "api_key"}, cfg.OpenAI)
	require.False(t, cfg.MCPExternal.Enabled)

	configData, err := os.ReadFile(filepath.Join(workspace, defaultConfigPath))
	require.NoError(t, err)
	var generated map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(configData, &generated))
	for _, removed := range []string{"thread_agents", "pre_seed", "context_messages", "seed_compaction_model"} {
		require.NotContains(t, string(configData), `"`+removed+`"`)
	}
	var slack map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(generated["slack"], &slack))
	for _, removed := range []string{"enabled", "room", "human_user_id", "allowed_user_ids", "social_mode"} {
		require.NotContains(t, slack, removed)
	}
	require.NotContains(t, output, "Enable Slack")

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

	ignoreData, err := os.ReadFile(filepath.Join(workspace, config.DefaultRuntimeDir, ".gitignore"))
	require.NoError(t, err)
	require.Contains(t, string(ignoreData), "auth.json")
}

func TestRunSetupConfiguresExternalMCPWithSlack(t *testing.T) {
	workspace, cfg := runSetupWithInput(t, slackMCPSetupInput("", "n"), nil)

	require.True(t, cfg.MCPExternal.Enabled)
	require.Equal(t, "127.0.0.1:8765", cfg.MCPExternal.ListenAddr)

	_, err := os.Stat(filepath.Join(workspace, externalMCPUsersFilename))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRunSetupAcceptsConfiguredOpenAIBaseURL(t *testing.T) {
	_, cfg := runSetupWithInput(t, slackSetupInput("https://api.example/v1"), nil)

	require.Equal(t, "https://api.example/v1", cfg.OpenAI.APIBaseURL)
}

func TestRunSetupAcceptsConfiguredExternalMCPListenAddr(t *testing.T) {
	_, cfg := runSetupWithInput(t, slackMCPSetupInput("127.0.0.1:9999", "n"), nil)

	require.True(t, cfg.MCPExternal.Enabled)
	require.Equal(t, "127.0.0.1:9999", cfg.MCPExternal.ListenAddr)
}

func TestRunSetupPromptReadErrors(t *testing.T) {
	for _, tt := range []struct {
		name    string
		input   string
		wantErr string
	}{
		{name: "external mcp enablement", wantErr: "prompt external MCP enablement"},
		{name: "common fields", input: "n\n", wantErr: "read prompt input"},
		{name: "slack fields", input: strings.Join([]string{"n", "sk-test", "", "Ulderico", "Maschine"}, "\n") + "\n", wantErr: "read prompt input"},
		{name: "external mcp listen address", input: strings.Join([]string{"y", "sk-test", "", "Ulderico", "Maschine", "xoxb-test", "xapp-test", "ops", "U123"}, "\n") + "\n", wantErr: "read prompt input"},
		{name: "external mcp users file", input: strings.Join([]string{"y", "sk-test", "", "Ulderico", "Maschine", "xoxb-test", "xapp-test", "ops", "U123", "127.0.0.1:8765"}, "\n") + "\n", wantErr: "prompt external MCP users file creation"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			cwd, err := os.Getwd()
			require.NoError(t, err)
			require.NoError(t, os.Chdir(workspace))
			t.Cleanup(func() { require.NoError(t, os.Chdir(cwd)) })

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

			err = runSetup(nil)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestRunSetupWritesExternalMCPUsersFile(t *testing.T) {
	workspace, _, output := runSetupWithInputOutput(t, slackMCPSetupInput("", "y"), nil)

	info, err := os.Stat(filepath.Join(workspace, externalMCPUsersFilename))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	data, err := os.ReadFile(filepath.Join(workspace, externalMCPUsersFilename))
	require.NoError(t, err)
	var users map[string]string
	require.NoError(t, json.Unmarshal(data, &users))
	require.NotEmpty(t, users["admin"])
	require.Contains(t, output, "Wrote "+externalMCPUsersFilename)
	require.NotContains(t, output, users["admin"])
}

func TestRunSetupPreservesExistingRootSetupFiles(t *testing.T) {
	workspace, _ := runSetupWithInput(t, slackSetupInput(""), func(workspace string) {
		for _, name := range []string{"AGENTS.md", "main-update-cortex.sh"} {
			require.NoError(t, os.WriteFile(filepath.Join(workspace, name), []byte(name+" preserved\n"), 0o755))
		}
		require.NoError(t, os.MkdirAll(filepath.Join(workspace, "agents"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", "main.md"), []byte("%AGENT_NAME% preserved\n"), 0o644))
	})

	for _, name := range []string{"AGENTS.md", "main-update-cortex.sh"} {
		data, err := os.ReadFile(filepath.Join(workspace, name))
		require.NoError(t, err)
		require.Equal(t, name+" preserved\n", string(data))
	}

	data, err := os.ReadFile(filepath.Join(workspace, "agents", "main.md"))
	require.NoError(t, err)
	require.Equal(t, "%AGENT_NAME% preserved\n", string(data))
}

func TestRunSetupFilesListShowsKnownFiles(t *testing.T) {
	output := captureStdout(t, func() error { return runSetup([]string{"files", "list"}) })

	require.Contains(t, output, "AGENTS.md\n")
	require.Contains(t, output, "main-update-cortex.sh\n")
	require.NotContains(t, output, "main-split-markdown-files.sh\n")
	require.Contains(t, output, "agents/main.md\n")
	require.Contains(t, output, ".rocketclaw/skills/main-create-or-update-agent/SKILL.md\n")
	require.Contains(t, output, ".rocketclaw/skills/main-create-or-update-council/SKILL.md\n")
}

func TestRunSetupFilesGetReturnsEmbeddedContent(t *testing.T) {
	output := captureStdout(t, func() error { return runSetup([]string{"files", "get", "AGENTS.md"}) })

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
	t.Cleanup(func() { require.NoError(t, os.Chdir(cwd)) })

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

	output = captureStdout(t, func() error { return runSetup(nil) })

	configData, err := os.ReadFile(filepath.Join(workspace, defaultConfigPath))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(configData, &cfg))

	return workspace, cfg, output
}

func slackSetupInput(apiBase string) string {
	return strings.Join([]string{
		"n",
		"sk-test",
		apiBase,
		"Ulderico",
		"Maschine",
		"xoxb-test",
		"xapp-test",
		"ops",
		"U123",
	}, "\n") + "\n"
}

func slackMCPSetupInput(listenAddr, createExternalMCPUsers string) string {
	return strings.Join([]string{
		"y",
		"sk-test",
		"",
		"Ulderico",
		"Maschine",
		"xoxb-test",
		"xapp-test",
		"ops",
		"U123",
		listenAddr,
		createExternalMCPUsers,
	}, "\n") + "\n"
}
