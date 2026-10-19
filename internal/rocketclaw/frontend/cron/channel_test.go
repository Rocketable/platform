package cronfrontend

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A cron without a channel line posts nowhere; a channel line that names no channel is a mistake.
func TestLoadDefinitionChannelIsOptional(t *testing.T) {
	for _, frontmatter := range []string{"schedule: 1h", "schedule: 1h\nslack-channel: '#legacy'"} {
		definition, err := loadDefinition([]byte("---\n"+frontmatter+"\n---\nBody"), "cron/test.md")
		require.NoError(t, err)
		require.Empty(t, definition.textChannel)
	}

	for _, frontmatter := range []string{
		"schedule: 1h\nchannel: ''",
		"schedule: 1h\nchannel: null",
		"schedule: 1h\nchannel: #ops",
		"schedule: 1h\nchannel: 123",
	} {
		_, err := loadDefinition([]byte("---\n"+frontmatter+"\n---\nBody"), "cron/test.md")
		require.ErrorContains(t, err, "channel must name a Slack channel; leave it out to post nowhere", frontmatter)
	}
}

func TestValidateRuntimeDefinitionsRequiresConfiguredChannel(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(workspace, "cron"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "cron", "daily.md"), []byte("---\nschedule: 1h\nchannel: '#ops'\n---\nBody"), 0o644))

	require.ErrorContains(t, ValidateRuntimeDefinitions(workspace, ".", []string{"#release"}), "is not configured")
	require.NoError(t, ValidateRuntimeDefinitions(workspace, ".", []string{"#ops"}))
	// A cron without a channel needs none configured.
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "cron", "quiet.md"), []byte("---\nschedule: 1h\n---\nBody"), 0o644))
	require.NoError(t, ValidateRuntimeDefinitions(workspace, ".", []string{"#ops"}))

	m := New(workspace, ".", []string{"#release"}, newCronScheduleStore(t), &runnerMock{}, slog.New(slog.DiscardHandler))
	require.ErrorContains(t, m.Start(t.Context()), "channel \"#ops\" is not configured")
	jobs, err := m.Jobs()
	require.ErrorContains(t, err, "channel \"#ops\" is not configured")
	require.Nil(t, jobs)
}

func TestLoadDefinitionNormalizesConfiguredChannel(t *testing.T) {
	definition, err := loadDefinition([]byte("---\nschedule: 1h\nchannel: ' ops '\n---\nBody"), "cron/test.md")
	require.NoError(t, err)
	assert.Equal(t, "#ops", definition.textChannel)
}

// Only a channel's runs are told their reply is posted; a run with no channel posts nowhere.
func TestScheduledRunPromptFollowsChannel(t *testing.T) {
	for channel, want := range map[string]string{"#ops": "Body\n\n" + cronReplyInstruction, "": "Body"} {
		runner := &runnerMock{RunFunc: func(context.Context, string, string, *backend.RawRunProgress) (protocol.CronRunResult, error) {
			return protocol.CronRunResult{}, nil
		}}
		m := New(t.TempDir(), ".", nil, newCronScheduleStore(t), runner, slog.New(slog.DiscardHandler))
		m.executeJob(t.Context(), &definition{relativePath: "cron/daily.md", agent: "helper", textChannel: channel, body: "Body"})

		require.Len(t, runner.RunCalls(), 1)
		require.Equal(t, want, runner.RunCalls()[0].S1)
		require.Equal(t, channel, runner.RunCalls()[0].RawRunProgress.TextChannel)
	}
}
