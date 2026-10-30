package harness

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func TestLoadParsedAgentsAndSkillsAllowsMutation(t *testing.T) {
	agentsFS := fstest.MapFS{
		"main.md": {Data: []byte(`---
description: Main
model: gpt-5.4
permission:
  tools:
    current_time: deny
---
Prompt
`)},
	}
	skillsFS := fstest.MapFS{
		"docs-helper/SKILL.md": {Data: []byte(`---
name: docs-helper
description: Write docs
---
Use for docs.
`)},
	}

	agentResult := LoadAgents(agentsFS)
	skillResult := LoadSkills(skillsFS, "/virtual/skills")
	agents, skills := agentResult.Agents, skillResult.Skills

	require.Equal(t, "Prompt", agents.Items["main"].Prompt)
	require.Equal(t, "docs-helper", skills.Items["docs-helper"].Name)

	agent := agents.Items["main"]
	require.NoError(t, agent.Permission.Allow("tools", "current_time"))
	agents.Items["main"] = agent

	decision := agents.Items["main"].Permission.Buckets[0].Rules[1]
	require.Equal(t, PermissionRule{Pattern: "current_time", Action: PermissionAllow}, decision)
}
