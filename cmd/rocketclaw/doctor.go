package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func runDoctor(args []string) error {
	flagSet := flag.NewFlagSet("doctor", flag.ContinueOnError)
	if err := flagSet.Parse(args); err != nil {
		return fmt.Errorf("parse doctor flags: %w", err)
	}

	selected, cfg, err := loadRuntimeConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	lines := []string{
		fmt.Sprintf("Configuration: OK (%s)", selected.Path),
		"Workspace: " + cfg.Workspace,
		"Work directory: " + cfg.RuntimeDirName(),
		fmt.Sprintf("Slack: %t", cfg.Slack.Enabled),
		fmt.Sprintf("External MCP: %t", cfg.MCPExternal.Enabled),
		"RocketCode: OK (library)",
	}

	if _, err := fmt.Fprint(os.Stdout, strings.Join(lines, "\n")+"\n"); err != nil {
		return fmt.Errorf("write doctor output: %w", err)
	}

	return nil
}
