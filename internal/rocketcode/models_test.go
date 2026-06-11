package rocketcode

import (
	"os"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	openai "github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/require"
)

func TestParseModelRef(t *testing.T) {
	for _, tc := range []struct {
		name     string
		model    string
		provider string
		apiModel string
		display  string
	}{
		{name: "empty", model: "", provider: "openai", apiModel: "gpt-5.4", display: "openai/gpt-5.4"},
		{name: "unprefixed", model: "gpt-5.5", provider: "openai", apiModel: "gpt-5.5", display: "openai/gpt-5.5"},
		{name: "openai", model: "openai/gpt-5.5", provider: "openai", apiModel: "gpt-5.5", display: "openai/gpt-5.5"},
		{name: "anthropic", model: "anthropic/claude-sonnet-4-20250514", provider: "anthropic", apiModel: "claude-sonnet-4-20250514", display: "anthropic/claude-sonnet-4-20250514"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := parseModelRef(tc.model)

			require.NoError(t, err)
			require.Equal(t, tc.provider, parsed.provider)
			require.Equal(t, tc.apiModel, parsed.apiModel)
			require.Equal(t, tc.display, parsed.display())
		})
	}
}

func TestParseModelRefRejectsInvalidProvider(t *testing.T) {
	for _, model := range []string{"anthropic/", "/model", "bogus/model"} {
		t.Run(model, func(t *testing.T) {
			_, err := parseModelRef(model)

			require.Error(t, err)
		})
	}
}

func TestNewWithProvidersRoutesAnthropicModel(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	config := testConfig(dir)
	config.Model = "anthropic/claude-sonnet-4-20250514"
	client := anthropic.NewClient(anthropicoption.WithAPIKey("test-key"))
	loop, err := NewWithProviders(Providers{Anthropic: &client}, config, root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Prompt: "prompt"},
	}}, Skills{Items: map[string]Skill{}}, "main", nil)

	require.NoError(t, err)
	require.Equal(t, "claude-sonnet-4-20250514", loop.Model)
	require.Equal(t, "anthropic/claude-sonnet-4-20250514", loop.DisplayModel)
}

func TestNewWithProvidersRequiresSelectedProvider(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	config := testConfig(dir)
	config.Model = "anthropic/claude-sonnet-4-20250514"
	openAIClient := openai.NewClient()
	_, err = NewWithProviders(Providers{OpenAI: &openAIClient}, config, root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Prompt: "prompt"},
	}}, Skills{Items: map[string]Skill{}}, "main", nil)

	require.EqualError(t, err, "anthropic provider is required")
}
