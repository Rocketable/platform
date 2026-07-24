package harnessbridge

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseGoalRequestMaxTurnsDefaultAndOverrides(t *testing.T) {
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
			goal, rejection := ParseGoalRequest(tt.text)
			require.Empty(t, rejection)
			assert.Equal(t, tt.want, goal.MaxTurns)
		})
	}
}

func TestParseGoalRequestAttachedParameters(t *testing.T) {
	for _, tt := range []struct {
		name, text               string
		wantObjective, wantCheck string
		wantMaxTurns             int
	}{
		{
			name:          "check script",
			text:          "checkScript:./scripts/check.sh ship the release",
			wantObjective: "ship the release",
			wantCheck:     "./scripts/check.sh",
			wantMaxTurns:  5,
		},
		{
			name:          "quoted check script",
			text:          "checkScript:\"./scripts/check.sh --full\" ship the release",
			wantObjective: "ship the release",
			wantCheck:     "./scripts/check.sh --full",
			wantMaxTurns:  5,
		},
		{
			name:          "both parameters",
			text:          "maxTurns:2 checkScript:./scripts/check.sh ship the release",
			wantObjective: "ship the release",
			wantCheck:     "./scripts/check.sh",
			wantMaxTurns:  2,
		},
		{
			name:          "after objective",
			text:          "ship maxTurns:2 checkScript:./scripts/check.sh",
			wantObjective: "ship maxTurns:2 checkScript:./scripts/check.sh",
			wantMaxTurns:  5,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			goal, rejection := ParseGoalRequest(tt.text)
			require.Empty(t, rejection)
			assert.Equal(t, tt.wantObjective, goal.Objective)
			assert.Equal(t, tt.wantCheck, goal.CheckScript)
			assert.Equal(t, tt.wantMaxTurns, goal.MaxTurns)
		})
	}
}

func TestParseGoalRequestUsesCanonicalExamples(t *testing.T) {
	_, rejection := ParseGoalRequest("")
	assert.Contains(t, rejection, "`$goal`")

	_, rejection = ParseGoalRequest("maxTurns: 5")
	assert.Contains(t, rejection, "`$goal maxTurns: 5 update the docs`")
}

func TestParseGoalRequestRejectsMalformedArguments(t *testing.T) {
	for _, text := range []string{
		"maxTurns:",
		"maxTurns: nope goal",
		"checkScript:",
		"checkScript: \"\" fix lint",
		"checkScript: \"./scripts/check.sh fix lint",
		`checkScript: "$(./scripts/check.sh)" fix lint`,
	} {
		t.Run(text, func(t *testing.T) {
			_, rejection := ParseGoalRequest(text)
			assert.NotEmpty(t, rejection)
		})
	}
}
