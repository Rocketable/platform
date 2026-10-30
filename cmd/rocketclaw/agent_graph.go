package main

import (
	"github.com/Rocketable/platform/internal/rocketclaw/agentlint"
)

func runAgentGraph(args []string) error {
	target, secretsARN, err := parseInspectionCommand("agent-graph", args)
	if err != nil {
		return err
	}

	runtimeRoot, _, cleanup, err := runtimeRootForInspectionTarget(target, "rocketclaw-agent-graph-*", "agent graph", secretsARN)
	if err != nil {
		return err
	}
	defer cleanup()

	dot, err := agentlint.AgentGraphDOT(runtimeRoot)
	if err != nil {
		return err
	}

	return printStdout(dot, "agent graph")
}
