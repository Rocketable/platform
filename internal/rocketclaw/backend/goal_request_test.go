package backend

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

func TestParseGoalRequestMaxTurnsDefaultAndOverrides(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name, text string
		want       int
	}{
		{name: "omitted", text: "update the docs", want: 5},
		{name: "explicit", text: "maxTurns: 20 update the docs", want: 20},
		{name: "attached", text: "maxTurns:20 update the docs", want: 20},
		{name: "zero", text: "maxTurns: 0 update the docs", want: 0},
		{name: "negative one", text: "maxTurns: -1 update the docs", want: 0},
		{name: "infinite", text: "maxTurns: infinite update the docs", want: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			goal, rejection := protocol.ParseGoalRequest(tt.text)
			require.Empty(t, rejection)
			assert.Equal(t, tt.want, goal.MaxTurns)
		})
	}
}

func TestParseGoalRequestKeepsParametersAfterObjective(t *testing.T) {
	t.Parallel()

	goal, rejection := protocol.ParseGoalRequest("ship maxTurns:2")
	require.Empty(t, rejection)
	assert.Equal(t, protocol.GoalRequest{Objective: "ship maxTurns:2", MaxTurns: 5}, goal)
}

func TestParseGoalRequestUsesCanonicalExamples(t *testing.T) {
	t.Parallel()

	_, rejection := protocol.ParseGoalRequest("")
	assert.Equal(t, protocol.GoalCommandHelp, rejection)
	assert.Contains(t, rejection, "maxTurns:")
	assert.Contains(t, rejection, "$goal maxTurns: 10 ship the release")

	_, rejection = protocol.ParseGoalRequest("maxTurns: 5")
	assert.Contains(t, rejection, "`$goal maxTurns: 5 update the docs`")
}

func TestParseGoalRequestRejectsMalformedArguments(t *testing.T) {
	t.Parallel()

	for _, text := range []string{
		"maxTurns:",
		"maxTurns: nope goal",
	} {
		t.Run(text, func(t *testing.T) {
			_, rejection := protocol.ParseGoalRequest(text)
			assert.NotEmpty(t, rejection)
		})
	}
}
