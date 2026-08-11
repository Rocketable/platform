package rocketcode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func TestStandaloneConfigFromEnvDefaults(t *testing.T) {
	t.Setenv("ROCKETCODE_MODEL", "")
	t.Setenv("ROCKETCODE_AUTO_APPROVER_MODEL", "")
	t.Setenv("ROCKETCODE_REASONING_EFFORT", "")
	t.Setenv("ROCKETCODE_DIAG", "")
	t.Setenv("ROCKETCODE_EXPERIMENTAL_STRONGER_SKILLS", "")
	t.Setenv("ROCKETCODE_EXPAND_PROMPT_SHELL_COMMANDS", "")
	t.Setenv("ROCKETCODE_COMPACT_THRESHOLD", "")
	t.Setenv("ROCKETCODE_COMPACTION_STEERING", "")
	t.Setenv("ROCKETCODE_AUTO_APPROVE_PERMISSIONS", "")

	config, err := StandaloneConfigFromEnv()

	require.NoError(t, err)
	require.Equal(t, "gpt-5.5", config.Model)
	require.Empty(t, config.AutoApproverModel)
	require.Equal(t, "high", string(config.ReasoningEffort))
	require.False(t, config.Diagnostics)
	require.False(t, config.ExperimentalStrongerSkills)
	require.False(t, config.AutoApprovePermissions)
	require.Equal(t, PromptShellCommandExpansion{PrimaryPrompts: false, SubagentPrompts: false, SkillPrompts: false, InputPrompts: false}, config.ExpandPromptShellCommands)
	require.Equal(t, int64(200000), config.CompactThreshold)
	require.Empty(t, config.CompactionSteering)
	require.Equal(t, filepath.Join(".tmp", "shell-outputs"), config.ShellOutputDir)
	require.False(t, config.SandboxedBash)
	require.Len(t, config.CustomTools, 1)
	require.Equal(t, "current_time", config.CustomTools[0].Name)

	result, err := config.CustomTools[0].Call(context.Background(), json.RawMessage(`{}`), nil)
	require.NoError(t, err)
	require.NotEmpty(t, result.Output)
}

func TestStandaloneConfigFromEnvReadsOverrides(t *testing.T) {
	t.Setenv("ROCKETCODE_MODEL", "custom-model")
	t.Setenv("ROCKETCODE_AUTO_APPROVER_MODEL", "review-model")
	t.Setenv("ROCKETCODE_REASONING_EFFORT", "low")
	t.Setenv("ROCKETCODE_DIAG", "1")
	t.Setenv("ROCKETCODE_EXPERIMENTAL_STRONGER_SKILLS", "1")
	t.Setenv("ROCKETCODE_EXPAND_PROMPT_SHELL_COMMANDS", "primary,skill")
	t.Setenv("ROCKETCODE_COMPACT_THRESHOLD", "12345")
	t.Setenv("ROCKETCODE_COMPACTION_STEERING", "fresh compaction instructions")
	t.Setenv("ROCKETCODE_AUTO_APPROVE_PERMISSIONS", "1")

	config, err := StandaloneConfigFromEnv()

	require.NoError(t, err)
	require.Equal(t, "custom-model", config.Model)
	require.Equal(t, "review-model", config.AutoApproverModel)
	require.Equal(t, "low", string(config.ReasoningEffort))
	require.True(t, config.Diagnostics)
	require.True(t, config.ExperimentalStrongerSkills)
	require.True(t, config.AutoApprovePermissions)
	require.Equal(t, PromptShellCommandExpansion{PrimaryPrompts: true, SubagentPrompts: false, SkillPrompts: true, InputPrompts: false}, config.ExpandPromptShellCommands)
	require.Equal(t, int64(12345), config.CompactThreshold)
	require.Equal(t, "fresh compaction instructions", config.CompactionSteering)
	require.Equal(t, filepath.Join(".tmp", "shell-outputs"), config.ShellOutputDir)
}

func TestStandaloneConfigFromEnvParsesPromptShellCommandExpansion(t *testing.T) {
	t.Run("all", func(t *testing.T) {
		t.Setenv("ROCKETCODE_EXPAND_PROMPT_SHELL_COMMANDS", "all")
		t.Setenv("ROCKETCODE_COMPACT_THRESHOLD", "")

		config, err := StandaloneConfigFromEnv()

		require.NoError(t, err)
		require.Equal(t, PromptShellCommandExpansion{PrimaryPrompts: true, SubagentPrompts: true, SkillPrompts: true, InputPrompts: false}, config.ExpandPromptShellCommands)
	})

	t.Run("legacy true", func(t *testing.T) {
		t.Setenv("ROCKETCODE_EXPAND_PROMPT_SHELL_COMMANDS", "1")
		t.Setenv("ROCKETCODE_COMPACT_THRESHOLD", "")

		config, err := StandaloneConfigFromEnv()

		require.NoError(t, err)
		require.Equal(t, PromptShellCommandExpansion{PrimaryPrompts: true, SubagentPrompts: true, SkillPrompts: true, InputPrompts: false}, config.ExpandPromptShellCommands)
	})

	t.Run("specific scopes", func(t *testing.T) {
		t.Setenv("ROCKETCODE_EXPAND_PROMPT_SHELL_COMMANDS", "primary, subagent, input")
		t.Setenv("ROCKETCODE_COMPACT_THRESHOLD", "")

		config, err := StandaloneConfigFromEnv()

		require.NoError(t, err)
		require.Equal(t, PromptShellCommandExpansion{PrimaryPrompts: true, SubagentPrompts: true, SkillPrompts: false, InputPrompts: true}, config.ExpandPromptShellCommands)
	})

	t.Run("disabled", func(t *testing.T) {
		t.Setenv("ROCKETCODE_EXPAND_PROMPT_SHELL_COMMANDS", "false")
		t.Setenv("ROCKETCODE_COMPACT_THRESHOLD", "")

		config, err := StandaloneConfigFromEnv()

		require.NoError(t, err)
		require.Equal(t, PromptShellCommandExpansion{PrimaryPrompts: false, SubagentPrompts: false, SkillPrompts: false, InputPrompts: false}, config.ExpandPromptShellCommands)
	})

	t.Run("rejects unknown scope", func(t *testing.T) {
		t.Setenv("ROCKETCODE_EXPAND_PROMPT_SHELL_COMMANDS", "primary,unknown")
		t.Setenv("ROCKETCODE_COMPACT_THRESHOLD", "")

		_, err := StandaloneConfigFromEnv()

		require.EqualError(t, err, `ROCKETCODE_EXPAND_PROMPT_SHELL_COMMANDS contains unknown value "unknown": expected primary, subagent, skill, input, or all`)
	})
}

func TestStandaloneConfigFromEnvRejectsInvalidCompactThreshold(t *testing.T) {
	for _, value := range []string{"nope", "0", "-1"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("ROCKETCODE_COMPACT_THRESHOLD", value)

			_, err := StandaloneConfigFromEnv()

			require.EqualError(t, err, "ROCKETCODE_COMPACT_THRESHOLD must be a positive integer")
		})
	}
}

func TestStandaloneConfigAutoApprovePermissionsFromEnv(t *testing.T) {
	t.Run("empty leaves disabled", func(t *testing.T) {
		t.Setenv("ROCKETCODE_AUTO_APPROVE_PERMISSIONS", "")

		config, err := StandaloneConfigFromEnv()

		require.NoError(t, err)
		require.False(t, config.AutoApprovePermissions)
	})

	t.Run("non empty enables", func(t *testing.T) {
		t.Setenv("ROCKETCODE_AUTO_APPROVE_PERMISSIONS", "1")

		config, err := StandaloneConfigFromEnv()

		require.NoError(t, err)
		require.True(t, config.AutoApprovePermissions)
	})
}

func TestStandaloneProvidersFromEnvConfiguresOpenAI(t *testing.T) {
	providers, err := StandaloneProvidersFromEnv()

	require.NoError(t, err)
	require.NotNil(t, providers.OpenAI)
}

func TestLoadWorkspaceDefinitionsReportsAgentLoadErrors(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.Mkdir("agents", 0o755))
	require.NoError(t, root.Mkdir("skills", 0o755))
	require.NoError(t, root.WriteFile("agents/main.md", []byte("---\ndescription: Main\n---\nPrompt\n"), 0o644))

	_, _, cleanup, err := LoadWorkspaceDefinitions(root)
	defer cleanup()

	require.ErrorContains(t, err, "main.md: model: required non-empty string")
}

func TestLoadParsedAgentsAndSkillsAllowsMutation(t *testing.T) {
	agentsFS := fstest.MapFS{
		"main.md": {Data: []byte(`---
description: Main
model: gpt-5.4
permission:
  tools:
    current_time: deny
---
Prompt
`)},
	}
	skillsFS := fstest.MapFS{
		"docs-helper/SKILL.md": {Data: []byte(`---
name: docs-helper
description: Write docs
---
Use for docs.
`)},
	}

	agentResult := LoadAgents(agentsFS, func(model string) (string, error) { return model, nil })
	skillResult := LoadSkills(skillsFS, "/virtual/skills")
	agents, skills := agentResult.Agents, skillResult.Skills

	require.Equal(t, "Prompt", agents.Items["main"].Prompt)
	require.Equal(t, "docs-helper", skills.Items["docs-helper"].Name)

	agent := agents.Items["main"]
	require.NoError(t, agent.Permission.Allow("tools", "current_time"))
	agents.Items["main"] = agent

	decision := agents.Items["main"].Permission.Buckets[0].Rules[1]
	require.Equal(t, PermissionRule{Pattern: "current_time", Action: PermissionAllow}, decision)
}

func TestSplitPromptAttachmentTokens(t *testing.T) {
	prompt, files, err := SplitPromptAttachmentTokens("look @attach:image.png now")

	require.NoError(t, err)
	require.Equal(t, "look now", prompt)
	require.Equal(t, []string{"image.png"}, files)

	_, _, err = SplitPromptAttachmentTokens("broken @attach:")
	require.EqualError(t, err, "@attach requires a file path")
}

func TestPromptAttachmentsReadsImageAndPDF(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.WriteFile("image.png", []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, 0o644))
	require.NoError(t, root.WriteFile("doc.pdf", []byte("%PDF-1.7\n"), 0o644))

	attachments, err := PromptAttachments(root, dir, []string{filepath.Join(dir, "image.png"), filepath.Join(dir, "doc.pdf")})

	require.NoError(t, err)
	require.Len(t, attachments, 2)
	require.Equal(t, "image/png", attachments[0].MIME)
	require.Equal(t, "application/pdf", attachments[1].MIME)
}
