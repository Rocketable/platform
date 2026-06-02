//nolint:exhaustruct // Tests intentionally use sparse fixtures for persisted session records.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Rocketable/platform/internal/rocketcode"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
)

func TestConfigFromEnvDefaults(t *testing.T) {
	t.Setenv("ROCKETCODE_MODEL", "")
	t.Setenv("ROCKETCODE_REASONING_EFFORT", "")
	t.Setenv("ROCKETCODE_DIAG", "")
	t.Setenv("ROCKETCODE_EXPERIMENTAL_STRONGER_SKILLS", "")
	t.Setenv("ROCKETCODE_EXPAND_PROMPT_SHELL_COMMANDS", "")
	t.Setenv("ROCKETCODE_COMPACT_THRESHOLD", "")
	t.Setenv("ROCKETCODE_COMPACTION_STEERING", "")

	config, err := configFromEnv()

	require.NoError(t, err)
	require.Equal(t, openai.ChatModelGPT5_4, config.Model)
	require.Equal(t, "high", string(config.ReasoningEffort))
	require.False(t, config.Diagnostics)
	require.False(t, config.ExperimentalStrongerSkills)
	require.Equal(t, rocketcode.PromptShellCommandExpansion{PrimaryPrompts: false, SubagentPrompts: false, SkillPrompts: false, InputPrompts: false}, config.ExpandPromptShellCommands)
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

func TestConfigFromEnvReadsOverrides(t *testing.T) {
	t.Setenv("ROCKETCODE_MODEL", "custom-model")
	t.Setenv("ROCKETCODE_REASONING_EFFORT", "low")
	t.Setenv("ROCKETCODE_DIAG", "1")
	t.Setenv("ROCKETCODE_EXPERIMENTAL_STRONGER_SKILLS", "1")
	t.Setenv("ROCKETCODE_EXPAND_PROMPT_SHELL_COMMANDS", "primary,skill")
	t.Setenv("ROCKETCODE_COMPACT_THRESHOLD", "12345")
	t.Setenv("ROCKETCODE_COMPACTION_STEERING", "fresh compaction instructions")

	config, err := configFromEnv()

	require.NoError(t, err)
	require.Equal(t, "custom-model", config.Model)
	require.Equal(t, "low", string(config.ReasoningEffort))
	require.True(t, config.Diagnostics)
	require.True(t, config.ExperimentalStrongerSkills)
	require.Equal(t, rocketcode.PromptShellCommandExpansion{PrimaryPrompts: true, SubagentPrompts: false, SkillPrompts: true, InputPrompts: false}, config.ExpandPromptShellCommands)
	require.Equal(t, int64(12345), config.CompactThreshold)
	require.Equal(t, "fresh compaction instructions", config.CompactionSteering)
	require.Equal(t, filepath.Join(".tmp", "shell-outputs"), config.ShellOutputDir)
}

func TestConfigFromEnvParsesPromptShellCommandExpansion(t *testing.T) {
	t.Run("all", func(t *testing.T) {
		t.Setenv("ROCKETCODE_EXPAND_PROMPT_SHELL_COMMANDS", "all")
		t.Setenv("ROCKETCODE_COMPACT_THRESHOLD", "")

		config, err := configFromEnv()

		require.NoError(t, err)
		require.Equal(t, rocketcode.PromptShellCommandExpansion{PrimaryPrompts: true, SubagentPrompts: true, SkillPrompts: true, InputPrompts: false}, config.ExpandPromptShellCommands)
	})

	t.Run("legacy true", func(t *testing.T) {
		t.Setenv("ROCKETCODE_EXPAND_PROMPT_SHELL_COMMANDS", "1")
		t.Setenv("ROCKETCODE_COMPACT_THRESHOLD", "")

		config, err := configFromEnv()

		require.NoError(t, err)
		require.Equal(t, rocketcode.PromptShellCommandExpansion{PrimaryPrompts: true, SubagentPrompts: true, SkillPrompts: true, InputPrompts: false}, config.ExpandPromptShellCommands)
	})

	t.Run("specific scopes", func(t *testing.T) {
		t.Setenv("ROCKETCODE_EXPAND_PROMPT_SHELL_COMMANDS", "primary, subagent, input")
		t.Setenv("ROCKETCODE_COMPACT_THRESHOLD", "")

		config, err := configFromEnv()

		require.NoError(t, err)
		require.Equal(t, rocketcode.PromptShellCommandExpansion{PrimaryPrompts: true, SubagentPrompts: true, SkillPrompts: false, InputPrompts: true}, config.ExpandPromptShellCommands)
	})

	t.Run("disabled", func(t *testing.T) {
		t.Setenv("ROCKETCODE_EXPAND_PROMPT_SHELL_COMMANDS", "false")
		t.Setenv("ROCKETCODE_COMPACT_THRESHOLD", "")

		config, err := configFromEnv()

		require.NoError(t, err)
		require.Equal(t, rocketcode.PromptShellCommandExpansion{PrimaryPrompts: false, SubagentPrompts: false, SkillPrompts: false, InputPrompts: false}, config.ExpandPromptShellCommands)
	})

	t.Run("rejects unknown scope", func(t *testing.T) {
		t.Setenv("ROCKETCODE_EXPAND_PROMPT_SHELL_COMMANDS", "primary,unknown")
		t.Setenv("ROCKETCODE_COMPACT_THRESHOLD", "")

		_, err := configFromEnv()

		require.EqualError(t, err, `ROCKETCODE_EXPAND_PROMPT_SHELL_COMMANDS contains unknown value "unknown": expected primary, subagent, skill, input, or all`)
	})
}

func TestConfigFromEnvRejectsInvalidCompactThreshold(t *testing.T) {
	for _, value := range []string{"nope", "0", "-1"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("ROCKETCODE_COMPACT_THRESHOLD", value)

			_, err := configFromEnv()

			require.EqualError(t, err, "ROCKETCODE_COMPACT_THRESHOLD must be a positive integer")
		})
	}
}

func TestLoadParsedAgentsAndSkillsAllowsMutation(t *testing.T) {
	agentsFS := fstest.MapFS{
		"main.md": {Data: []byte(`---
description: Main
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

	agents, skills := loadParsedAgentsAndSkills(agentsFS, skillsFS, "/virtual/skills")

	require.Equal(t, "Prompt", agents.Items["main"].Prompt)
	require.Equal(t, "docs-helper", skills.Items["docs-helper"].Name)

	agent := agents.Items["main"]
	require.NoError(t, agent.Permission.Allow("tools", "current_time"))
	agents.Items["main"] = agent

	decision := agents.Items["main"].Permission.Buckets[0].Rules[1]
	require.Equal(t, rocketcode.PermissionRule{Pattern: "current_time", Action: rocketcode.PermissionAllow}, decision)
}

func TestPrintOutputPrefixesResponses(t *testing.T) {
	output := make(chan rocketcode.ChatResponse, 4)
	output <- rocketcode.ChatResponse{Kind: rocketcode.ChatResponseAssistantCommentary, Text: "working", Tool: nil, Subagent: nil, Provider: nil}

	output <- rocketcode.ChatResponse{Kind: rocketcode.ChatResponseAssistantTool, Text: "", Tool: &rocketcode.ToolDiagnostic{Phase: "call", Name: "bash", Arguments: json.RawMessage(`{"command":"pwd"}`), Result: "", Status: "", Action: nil}, Subagent: nil, Provider: nil}

	output <- rocketcode.ChatResponse{Kind: rocketcode.ChatResponseReasoningSummary, Text: "thinking", Tool: nil, Subagent: nil, Provider: nil}

	output <- rocketcode.ChatResponse{Kind: rocketcode.ChatResponseAssistantMessage, Text: "done", Tool: nil, Subagent: nil, Provider: nil}

	close(output)

	var out bytes.Buffer

	err := printOutput(&out, output)

	require.NoError(t, err)
	require.Equal(t, "[assistant commentary] working\n[assistant tool] {\"tool\":{\"phase\":\"call\",\"name\":\"bash\",\"arguments\":{\"command\":\"pwd\"}}}\n[reasoning summary] thinking\n[assistant message] done\n", out.String())
}

func TestScanInputReadsFullLines(t *testing.T) {
	got, _, err := collectScannedInput(t, "hello world\n\nlast line")

	require.NoError(t, err)
	require.Equal(t, []rocketcode.PromptInput{
		{Role: rocketcode.PromptInputRoleUser, Text: "hello world", Attachments: nil, Responses: nil},
		{Role: rocketcode.PromptInputRoleUser, Text: "", Attachments: nil, Responses: nil},
		{Role: rocketcode.PromptInputRoleUser, Text: "last line", Attachments: nil, Responses: nil},
	}, got)
}

func TestScanInputMarksDeveloperMessages(t *testing.T) {
	got, _, err := collectScannedInput(t, "developer: keep this\n  developer: spaced\nDeveloper: not developer\n")

	require.NoError(t, err)
	require.Equal(t, []rocketcode.PromptInput{
		{Role: rocketcode.PromptInputRoleDeveloper, Text: "keep this", Attachments: nil, Responses: nil},
		{Role: rocketcode.PromptInputRoleDeveloper, Text: "spaced", Attachments: nil, Responses: nil},
		{Role: rocketcode.PromptInputRoleUser, Text: "Developer: not developer", Attachments: nil, Responses: nil},
	}, got)
}

func TestScanInputExitsOnCommand(t *testing.T) {
	got, _, err := collectScannedInput(t, "hello\n/exit\nignored\n")

	require.NoError(t, err)
	require.Equal(t, []rocketcode.PromptInput{{Role: rocketcode.PromptInputRoleUser, Text: "hello", Attachments: nil, Responses: nil}}, got)
}

func TestScanInputAttachesInlineFiles(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.WriteFile("image.png", []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, 0o644))

	got, _, err := collectScannedInputWithRoot(t, "look @attach:image.png now\ndeveloper: inspect @attach:image.png\nsecond\n", root, dir)

	require.NoError(t, err)

	require.Len(t, got, 3)
	require.Equal(t, rocketcode.PromptInputRoleUser, got[0].Role)
	require.Equal(t, "look now", got[0].Text)
	require.Len(t, got[0].Attachments, 1)
	require.Equal(t, "image/png", got[0].Attachments[0].MIME)
	require.Equal(t, rocketcode.PromptInputRoleDeveloper, got[1].Role)
	require.Equal(t, "inspect", got[1].Text)
	require.Len(t, got[1].Attachments, 1)
	require.Equal(t, "image/png", got[1].Attachments[0].MIME)
	require.Equal(t, rocketcode.PromptInput{Role: rocketcode.PromptInputRoleUser, Text: "second", Attachments: nil, Responses: nil}, got[2])
}

func TestPromptAttachmentsReadsImageAndPDF(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.WriteFile("image.png", []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, 0o644))
	require.NoError(t, root.WriteFile("doc.pdf", []byte("%PDF-1.7\n"), 0o644))

	attachments, err := promptAttachments(root, dir, []string{filepath.Join(dir, "image.png"), filepath.Join(dir, "doc.pdf")})

	require.NoError(t, err)
	require.Len(t, attachments, 2)
	require.Equal(t, "image/png", attachments[0].MIME)
	require.Equal(t, "application/pdf", attachments[1].MIME)
}

func collectScannedInput(t *testing.T, text string) ([]rocketcode.PromptInput, string, error) {
	t.Helper()

	return collectScannedInputWithRoot(t, text, nil, "")
}

func collectScannedInputWithRoot(t *testing.T, text string, root *os.Root, cwd string) ([]rocketcode.PromptInput, string, error) {
	t.Helper()

	input := make(chan rocketcode.PromptInput)
	errc := make(chan error, 1)

	var out bytes.Buffer

	go func() {
		errc <- scanInput(strings.NewReader(text), &out, input, root, cwd)
	}()

	items := []rocketcode.PromptInput{}

	for item := range input {
		close(item.Responses)
		item.Responses = nil
		items = append(items, item)
	}

	return items, out.String(), <-errc
}

func TestOpenSessionStoresEntriesInSQLite(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)

	defer func() { _ = root.Close() }()

	session, err := openSession(root, dir)
	require.NoError(t, err)

	defer func() { _ = session.close() }()

	replayInput, err := rocketcode.ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		{OfMessage: &responses.EasyInputMessageParam{Role: "user", Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String("hello")}, Type: "message"}},
		{OfMessage: &responses.EasyInputMessageParam{Role: "assistant", Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String("hi")}, Phase: "final_answer", Type: "message"}},
	})
	require.NoError(t, err)

	entry := rocketcode.SessionEntry{
		Version:     1,
		Type:        "turn",
		Timestamp:   time.Unix(123, 456).UTC(),
		ResponseID:  "resp-1",
		Model:       "model-1",
		ReplayInput: replayInput,
	}

	require.NoError(t, session.out(entry))

	entries, err := collectSessionEntries(session.in)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, entry.Version, entries[0].Version)
	require.Equal(t, entry.Type, entries[0].Type)
	require.Equal(t, entry.Timestamp, entries[0].Timestamp)
	require.Equal(t, entry.ResponseID, entries[0].ResponseID)
	require.Equal(t, entry.Model, entries[0].Model)
	require.Len(t, entries[0].ReplayInput, 2)
	require.JSONEq(t, `{"content":"hello","role":"user","type":"message"}`, string(entries[0].ReplayInput[0]))
	require.JSONEq(t, `{"content":"hi","phase":"final_answer","role":"assistant","type":"message"}`, string(entries[0].ReplayInput[1]))

	_, err = root.Stat(".tmp/session.sqlite")
	require.NoError(t, err)
}

func collectSessionEntries(seq func(func(rocketcode.SessionEntry, error) bool)) ([]rocketcode.SessionEntry, error) {
	var firstErr error

	entries := slices.Collect(func(yield func(rocketcode.SessionEntry) bool) {
		for entry, err := range seq {
			if err != nil {
				firstErr = err
				return
			}

			if !yield(entry) {
				return
			}
		}
	})

	if firstErr != nil {
		return nil, firstErr
	}

	return entries, nil
}
