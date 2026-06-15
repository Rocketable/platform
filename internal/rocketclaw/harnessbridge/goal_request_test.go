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
		{name: "omitted", text: "🏁 update the docs", want: 5},
		{name: "explicit", text: "🏁 maxTurns: 20 update the docs", want: 20},
		{name: "zero", text: "🏁 maxTurns: 0 update the docs", want: 0},
		{name: "negative one", text: "🏁 maxTurns: -1 update the docs", want: 0},
		{name: "infinite", text: "🏁 maxTurns: infinite update the docs", want: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			goal, rejection, ok := ParseGoalRequest(tt.text)
			require.True(t, ok)
			require.Empty(t, rejection)
			assert.Equal(t, tt.want, goal.MaxTurns)
		})
	}
}
