package harnessbridge

import (
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
)

func TestCompactModelParsesOpenAIModel(t *testing.T) {
	model, err := compactModel("gpt-5.5")

	require.NoError(t, err)
	require.Equal(t, seedCompactionModel{apiModel: responses.ResponseCompactParamsModel("gpt-5.5")}, model)
}

func TestCompactModelDefaultsEmptyModel(t *testing.T) {
	model, err := compactModel("")

	require.NoError(t, err)
	require.Equal(t, seedCompactionModel{apiModel: defaultSeedCompactionModel}, model)
}

func TestCompactModelRejectsProviderQualifiedModel(t *testing.T) {
	_, err := compactModel("openai/gpt-5.5")

	require.EqualError(t, err, `invalid checkpoint model "openai/gpt-5.5": expected unprefixed OpenAI model ID`)
}
