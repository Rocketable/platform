package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
)

const externalMCPUsersFilename = "rocketclaw.users.json"

func runSetup(args []string) error {
	if len(args) > 0 {
		if args[0] == "files" {
			return runSetupFiles(args[1:])
		}

		return errors.New("setup accepts only the `files` subcommand")
	}

	workspace, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve current working directory: %w", err)
	}

	createRootAgents, err := missingFile(filepath.Join(workspace, "AGENTS.md"))
	if err != nil {
		return fmt.Errorf("stat AGENTS.md path: %w", err)
	}

	createMainAgentOverlay, err := missingFile(filepath.Join(workspace, "agents", "main.md"))
	if err != nil {
		return fmt.Errorf("stat overlay main agent path: %w", err)
	}

	cfg := new(config.Config)
	cfg.Workspace = workspace
	cfg.Logging.Level = "debug"

	setupNames, err := interviewSetup(cfg)
	if err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate generated configuration: %w", err)
	}

	if err := skel.SyncInWithOverlays(workspace, config.DefaultWorkDir, nil, newLogger("info")); err != nil {
		return fmt.Errorf("sync embedded setup files: %w", err)
	}

	if err := writeConfig(defaultConfigPath, cfg); err != nil {
		return fmt.Errorf("write generated configuration: %w", err)
	}

	if err := applySetupNames(workspace, setupNames, createRootAgents, createMainAgentOverlay); err != nil {
		return fmt.Errorf("apply setup names: %w", err)
	}

	if setupNames.createExternalMCPUsers {
		passwordBytes := make([]byte, 16)
		if _, err := rand.Read(passwordBytes); err != nil {
			return fmt.Errorf("generate external MCP admin password: %w", err)
		}

		usersData, err := json.MarshalIndent(map[string]string{"admin": hex.EncodeToString(passwordBytes)}, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal external MCP users JSON: %w", err)
		}

		usersData = append(usersData, '\n')
		if err := os.WriteFile(filepath.Join(workspace, externalMCPUsersFilename), usersData, 0o600); err != nil {
			return fmt.Errorf("write external MCP users file: %w", err)
		}
	}

	report := fmt.Sprintf("Wrote %s\nPrepared workspace setup files in %s\nPrepared .rocketclaw in %s\n", defaultConfigPath, workspace, workspace)
	if setupNames.createExternalMCPUsers {
		report += fmt.Sprintf("Wrote %s\n", externalMCPUsersFilename)
	}

	if _, err := fmt.Fprint(os.Stdout, report); err != nil {
		return fmt.Errorf("report setup result: %w", err)
	}

	return nil
}

func runSetupFiles(args []string) error {
	if len(args) == 0 {
		return errors.New("setup files requires `list` or `get <filename>`")
	}

	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("setup files list does not accept arguments")
		}

		files, err := skel.ListSetupFiles()
		if err != nil {
			return fmt.Errorf("list embedded setup files: %w", err)
		}

		if _, err := fmt.Fprintln(os.Stdout, strings.Join(files, "\n")); err != nil {
			return fmt.Errorf("print embedded setup file list: %w", err)
		}

		return nil
	case "get":
		if len(args) != 2 {
			return errors.New("setup files get accepts exactly one filename")
		}

		data, err := skel.ReadSetupFile(args[1])
		if err != nil {
			return fmt.Errorf("read embedded setup file %s: %w", args[1], err)
		}

		if _, err := os.Stdout.Write(data); err != nil {
			return fmt.Errorf("print embedded setup file %s: %w", args[1], err)
		}

		return nil
	default:
		return errors.New("setup files requires `list` or `get <filename>`")
	}
}

type setupNames struct {
	humanPartnerName, agentName string
	createExternalMCPUsers      bool
}

func interviewSetup(cfg *config.Config) (setupNames, error) {
	reader := bufio.NewReader(os.Stdin)

	var names setupNames

	for {
		slackEnabled, err := promptYesNoDefault(reader, "Enable Slack connector? [y/N]: ", false)
		if err != nil {
			return names, fmt.Errorf("prompt Slack enablement: %w", err)
		}

		externalMCPEnabled, err := promptYesNoDefault(reader, "Enable external MCP HTTP server? [y/N]: ", false)
		if err != nil {
			return names, fmt.Errorf("prompt external MCP enablement: %w", err)
		}

		if !slackEnabled && !externalMCPEnabled {
			if _, err := fmt.Fprintln(os.Stdout, "At least one connector or external MCP server must be enabled."); err != nil {
				return names, fmt.Errorf("report missing connector selection: %w", err)
			}

			continue
		}

		cfg.Slack.Enabled = slackEnabled
		cfg.MCPExternal.Enabled = externalMCPEnabled

		break
	}

	if err := promptFields(reader,
		promptField{prompt: "OpenAI API key: ", required: true, value: &cfg.OpenAI.APIKey},
		promptField{prompt: "OpenAI API base URL (leave blank for default): ", value: &cfg.OpenAI.APIBaseURL},
		promptField{prompt: "Human partner name: ", required: true, value: &names.humanPartnerName},
		promptField{prompt: "Agent name: ", required: true, value: &names.agentName},
	); err != nil {
		return names, err
	}

	if cfg.Slack.Enabled {
		if err := promptFields(reader,
			promptField{prompt: "Slack bot token: ", required: true, value: &cfg.Slack.BotToken},
			promptField{prompt: "Slack app token: ", required: true, value: &cfg.Slack.AppToken},
			promptField{prompt: "Slack DM room/channel ID: ", required: true, value: &cfg.Slack.Room},
			promptField{prompt: "Slack human partner user ID: ", required: true, value: &cfg.Slack.HumanUserID},
		); err != nil {
			return names, err
		}
	}

	if cfg.MCPExternal.Enabled {
		cfg.MCPExternal.ListenAddr = "127.0.0.1:8765"
		if err := promptFields(reader, promptField{prompt: "External MCP listen address (serves /mcp) [127.0.0.1:8765]: ", value: &cfg.MCPExternal.ListenAddr}); err != nil {
			return names, err
		}

		createExternalMCPUsers, err := promptYesNoDefault(reader, "Create rocketclaw.users.json with one generated admin user? [y/N]: ", false)
		if err != nil {
			return names, fmt.Errorf("prompt external MCP users file creation: %w", err)
		}

		names.createExternalMCPUsers = createExternalMCPUsers
	}

	return names, nil
}

type promptField struct {
	prompt   string
	value    *string
	required bool
}

func promptFields(reader *bufio.Reader, fields ...promptField) error {
	for _, field := range fields {
		for {
			value, err := promptInput(reader, field.prompt)
			if err != nil {
				return err
			}

			if value != "" {
				*field.value = value
				break
			}

			if !field.required {
				break
			}

			if _, err := fmt.Fprintln(os.Stdout, "This value is required."); err != nil {
				return fmt.Errorf("print required-value message: %w", err)
			}
		}
	}

	return nil
}

func promptInput(reader *bufio.Reader, prompt string) (string, error) {
	if _, err := fmt.Fprint(os.Stdout, prompt); err != nil {
		return "", fmt.Errorf("print prompt: %w", err)
	}

	text, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read prompt input: %w", err)
	}

	return strings.TrimSpace(text), nil
}

func promptYesNoDefault(reader *bufio.Reader, prompt string, defaultValue bool) (bool, error) {
	for {
		text, err := promptInput(reader, prompt)
		if err != nil {
			return false, err
		}

		switch strings.ToLower(text) {
		case "":
			return defaultValue, nil
		case "n", "no":
			return false, nil
		case "y", "yes":
			return true, nil
		default:
			if _, err := fmt.Fprintln(os.Stdout, "Please answer yes or no."); err != nil {
				return false, fmt.Errorf("print yes/no guidance: %w", err)
			}
		}
	}
}

func writeConfig(path string, cfg *config.Config) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve config output path: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return fmt.Errorf("create config parent directory: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config JSON: %w", err)
	}

	data = append(data, '\n')
	if err := os.WriteFile(absPath, data, 0o600); err != nil {
		return fmt.Errorf("write config file %s: %w", absPath, err)
	}

	return nil
}

func applySetupNames(workspace string, names setupNames, applyRootAgents, applyMainAgent bool) error {
	replacer := strings.NewReplacer(
		"%HUMAN_PARTNER_NAME%", names.humanPartnerName,
		"%AGENT_NAME%", names.agentName,
	)

	targets := make([]string, 0, 3)
	if applyRootAgents {
		targets = append(targets, "AGENTS.md")
	}

	if applyMainAgent {
		targets = append(targets,
			filepath.Join("agents", "main.md"),
			filepath.Join(".rocketclaw", "agents", "main.md"),
		)
	}

	for _, target := range targets {
		if err := replaceFilePlaceholders(filepath.Join(workspace, target), replacer); err != nil {
			return fmt.Errorf("replace placeholders in %s: %w", target, err)
		}
	}

	return nil
}

func replaceFilePlaceholders(path string, replacer *strings.Replacer) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat file: %w", err)
	}

	replaced := replacer.Replace(string(data))
	if err := os.WriteFile(path, []byte(replaced), info.Mode().Perm()); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	return nil
}
