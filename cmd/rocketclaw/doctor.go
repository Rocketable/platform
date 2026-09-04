package main

import (
	"flag"
	"fmt"
	"strings"
)

func runDoctor(args []string) error {
	flagSet := flag.NewFlagSet("doctor", flag.ContinueOnError)
	secretsARN := flagSet.String(secretsARNFlag, "", secretsARNUsage)
	if err := flagSet.Parse(args); err != nil {
		return fmt.Errorf("parse doctor flags: %w", err)
	}

	selected, cfg, err := loadRuntimeConfig(*secretsARN)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	lines := []string{
		fmt.Sprintf("Configuration: OK (%s)", selected.Path),
		"Workspace: " + cfg.Workspace,
		"Work directory: " + cfg.RuntimeDirName(),
		"Slack: active",
		fmt.Sprintf("External MCP: %t", cfg.MCPExternal.Enabled),
		"RocketCode: OK (library)",
	}

	return printStdout(strings.Join(lines, "\n")+"\n", "doctor output")
}
