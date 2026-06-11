package harnessbridge

import (
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
)

func TestCompactModelParsesProviderQualifiedOpenAI(t *testing.T) {
	model, err := compactModel("openai/gpt-5.5")

	require.NoError(t, err)
	require.Equal(t, responses.ResponseCompactParamsModel("gpt-5.5"), model)
}

func TestCompactModelDefaultsEmptyModel(t *testing.T) {
	model, err := compactModel("")

	require.NoError(t, err)
	require.Equal(t, responses.ResponseCompactParamsModelGPT5_4, model)
}

func TestCompactModelRejectsAnthropic(t *testing.T) {
	_, err := compactModel("anthropic/claude-sonnet-4-20250514")

	require.EqualError(t, err, `response checkpoint compaction does not support Anthropic model "anthropic/claude-sonnet-4-20250514"`)
}
