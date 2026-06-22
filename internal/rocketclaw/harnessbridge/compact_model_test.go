package harnessbridge

import (
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
)

func TestCompactModelParsesProviderQualifiedOpenAI(t *testing.T) {
	model, err := compactModel("openai/gpt-5.5")

	require.NoError(t, err)
	require.Equal(t, seedCompactionModel{provider: "openai", apiModel: responses.ResponseCompactParamsModel("gpt-5.5")}, model)
}

func TestCompactModelDefaultsEmptyModel(t *testing.T) {
	model, err := compactModel("")

	require.NoError(t, err)
	require.Equal(t, seedCompactionModel{provider: "openai", apiModel: responses.ResponseCompactParamsModelGPT5_4}, model)
}

func TestCompactModelParsesAnthropic(t *testing.T) {
	model, err := compactModel("anthropic/claude-sonnet-4-20250514")

	require.NoError(t, err)
	require.Equal(t, seedCompactionModel{provider: "anthropic", apiModel: responses.ResponseCompactParamsModel("claude-sonnet-4-20250514")}, model)
}

func TestCompactModelRejectsUnprefixedModel(t *testing.T) {
	_, err := compactModel("gpt-5.5")

	require.EqualError(t, err, `invalid checkpoint model "gpt-5.5": expected provider/model`)
}
