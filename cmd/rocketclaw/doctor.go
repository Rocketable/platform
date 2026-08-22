package main

import (
	"flag"
	"fmt"
	"os"
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

	devMCP := fmt.Sprintf("Development MCP: %t", cfg.MCPDevelopment.Enabled)
	if addr := strings.TrimSpace(cfg.MCPDevelopment.ListenAddr); addr != "" {
		devMCP += " " + addr
	}

	lines := []string{
		fmt.Sprintf("Configuration: OK (%s)", selected.Path),
		"Workspace: " + cfg.Workspace,
		"Work directory: " + cfg.RuntimeDirName(),
		"Slack: active",
		fmt.Sprintf("External MCP: %t", cfg.MCPExternal.Enabled),
		devMCP,
		"RocketCode: OK (library)",
	}

	if _, err := fmt.Fprint(os.Stdout, strings.Join(lines, "\n")+"\n"); err != nil {
		return fmt.Errorf("write doctor output: %w", err)
	}

	return nil
}
