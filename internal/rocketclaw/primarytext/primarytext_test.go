package primarytext

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseSocialAgentSwitch(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		agent string
		ok    bool
	}{
		{name: "alias no space", text: ":control_knobs:sudo", agent: "sudo", ok: true},
		{name: "alias with space", text: ":control_knobs: sudo", agent: "sudo", ok: true},
		{name: "bare alias", text: ":control_knobs:", ok: true},
		{name: "bare unicode text presentation", text: "🎛", ok: true},
		{name: "bare unicode emoji presentation", text: "🎛️", ok: true},
		{name: "unicode text presentation", text: "🎛 sudo", agent: "sudo", ok: true},
		{name: "unicode emoji presentation", text: "🎛️ sudo", agent: "sudo", ok: true},
		{name: "unicode no space", text: "🎛sudo", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent, ok := ParseSocialAgentSwitch(tt.text)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.agent, agent)
		})
	}
}
