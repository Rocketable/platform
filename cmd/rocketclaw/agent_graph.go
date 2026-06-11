package main

import (
	"fmt"

	"github.com/Rocketable/platform/internal/rocketclaw/agentlint"
)

func runAgentGraph(args []string) error {
	target := "next"
	if len(args) > 0 {
		target = args[0]
	}
	if len(args) > 1 || (target != "next" && target != "current") {
		return fmt.Errorf("usage: rocketclaw agent-graph [next|current]")
	}

	runtimeRoot, cleanup, err := runtimeRootForInspectionTarget(target, "rocketclaw-agent-graph-*", "agent graph")
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
