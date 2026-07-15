package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateSlackChannels(t *testing.T) {
	cfg := validConfig()
	cfg.Slack.Channels = []SlackChannelConfig{
		{Channel: " triage ", Agents: []string{" planner ", "", "planner", "helper"}, AllowedUserIDs: []string{" U999 ", "", "U999"}},
	}

	require.NoError(t, cfg.Validate())
	assert.Equal(t, []SlackChannelConfig{{Channel: "#triage", Agents: []string{"planner", "helper"}, AllowedUserIDs: []string{"U999"}}}, cfg.Slack.Channels)
}

func TestValidateSlackRequiresChannels(t *testing.T) {
	cfg := validConfig()
	cfg.Slack.Channels = nil

	require.ErrorContains(t, cfg.Validate(), "slack.channels is required")
}
