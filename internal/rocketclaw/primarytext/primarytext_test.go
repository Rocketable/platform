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

func TestNextSocialAgent(t *testing.T) {
	tests := []struct {
		name    string
		current string
		agents  []string
		want    string
	}{
		{name: "next", current: "triage", agents: []string{"triage", "planner", "reviewer"}, want: "planner"},
		{name: "wrap", current: "reviewer", agents: []string{"triage", "planner", "reviewer"}, want: "triage"},
		{name: "missing current", current: "retired", agents: []string{"triage", "planner"}, want: "triage"},
		{name: "trim current", current: " planner ", agents: []string{"triage", "planner", "reviewer"}, want: "reviewer"},
		{name: "empty agents", current: "triage", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, NextSocialAgent(tt.current, tt.agents))
		})
	}
}
