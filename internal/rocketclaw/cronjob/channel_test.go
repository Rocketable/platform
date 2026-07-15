package cronjob

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadDefinitionRequiresCanonicalChannel(t *testing.T) {
	for _, frontmatter := range []string{
		"schedule: 1h",
		"schedule: 1h\nchannel: ''",
		"schedule: 1h\nchannel: null",
		"schedule: 1h\nchannel: 123",
		"schedule: 1h\nslack-channel: '#legacy'",
	} {
		_, err := loadDefinition([]byte("---\n"+frontmatter+"\n---\nBody"), "cron/test.md")
		require.ErrorContains(t, err, "channel is required")
	}
}

func TestValidateRuntimeDefinitionsRequiresConfiguredChannel(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(workspace, "cron"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "cron", "daily.md"), []byte("---\nschedule: 1h\nchannel: '#ops'\n---\nBody"), 0o644))

	require.ErrorContains(t, ValidateRuntimeDefinitions(workspace, ".", []string{"#release"}), "is not configured")
	require.NoError(t, ValidateRuntimeDefinitions(workspace, ".", []string{"#ops"}))
}

func TestLoadDefinitionNormalizesConfiguredChannel(t *testing.T) {
	definition, err := loadDefinition([]byte("---\nschedule: 1h\nchannel: ' ops '\n---\nBody"), "cron/test.md")
	require.NoError(t, err)
	assert.Equal(t, "#ops", definition.textChannel)
}
