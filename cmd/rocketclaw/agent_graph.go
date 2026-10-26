package main

import "github.com/Rocketable/platform/internal/rocketclaw/protocol"

func runAgentGraph(args []string) error {
	dot, _, err := inspectAgents("agent-graph", protocol.InspectGraph, args)
	if err != nil {
		return err
	}

	return printStdout(dot, "agent graph")
}
