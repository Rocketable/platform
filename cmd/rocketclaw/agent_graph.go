package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/Rocketable/platform/internal/rocketclaw/agentlint"
)

func runAgentGraph(args []string) error {
	flagSet := flag.NewFlagSet("rocketclaw agent-graph", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)
	secretsARN := flagSet.String(secretsARNFlag, "", secretsARNUsage)
	if err := flagSet.Parse(args); err != nil {
		return fmt.Errorf("parse agent-graph flags: %w", err)
	}
	rest := flagSet.Args()
	target := "next"
	if len(rest) > 0 {
		target = rest[0]
	}
	if len(rest) > 1 || (target != "next" && target != "current") {
		return fmt.Errorf("usage: rocketclaw agent-graph [next|current]")
	}

	runtimeRoot, cfg, cleanup, err := runtimeRootForInspectionTarget(target, "rocketclaw-agent-graph-*", "agent graph", *secretsARN)
	if err != nil {
		return err
	}
	defer cleanup()

	dot, err := agentlint.AgentGraphDOT(runtimeRoot, cfg)
	if err != nil {
		return err
	}

	return printStdout(dot, "agent graph")
}
