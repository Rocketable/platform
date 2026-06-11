package rocketcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

// OperationError adds typed operation context while preserving the wrapped error.
type OperationError struct {
	Operation Operation
	Err       error
}

// Operation identifies a high-level command/runtime operation.
type Operation string

const (
	// OperationLoadConfig identifies standalone config loading.
	OperationLoadConfig Operation = "load config"
	// OperationLoadWorkspaceDefinitions identifies workspace-local agent/skill loading.
	OperationLoadWorkspaceDefinitions Operation = "load workspace definitions"
	// OperationParsePromptAttachments identifies prompt attachment token parsing.
	OperationParsePromptAttachments Operation = "parse prompt attachments"
	// OperationLoadPromptAttachments identifies workspace prompt attachment loading.
	OperationLoadPromptAttachments Operation = "load prompt attachments"
)

func (e OperationError) Error() string {
	return string(e.Operation) + ": " + e.Err.Error()
}

func (e OperationError) Unwrap() error {
	return e.Err
}

// StandaloneConfigFromEnv returns the shared default config for RocketCode commands.
func StandaloneConfigFromEnv() (Config, error) {
	config := standaloneDefaultConfig()

	if value := os.Getenv("ROCKETCODE_MODEL"); value != "" {
		config.Model = value
	}

	if value := os.Getenv("ROCKETCODE_REASONING_EFFORT"); value != "" {
		config.ReasoningEffort = shared.ReasoningEffort(value)
	}

	config.Diagnostics = os.Getenv("ROCKETCODE_DIAG") != ""
	config.ExperimentalStrongerSkills = os.Getenv("ROCKETCODE_EXPERIMENTAL_STRONGER_SKILLS") != ""

	expansion := strings.TrimSpace(os.Getenv("ROCKETCODE_EXPAND_PROMPT_SHELL_COMMANDS"))
	switch {
	case expansion == "" || expansion == "0" || strings.EqualFold(expansion, "false"):
	case expansion == "1" || strings.EqualFold(expansion, "true"):
		config.ExpandPromptShellCommands = PromptShellCommandExpansion{PrimaryPrompts: true, SubagentPrompts: true, SkillPrompts: true}
	default:
		for part := range strings.SplitSeq(expansion, ",") {
			token := strings.ToLower(strings.TrimSpace(part))
			switch token {
			case "":
				continue
			case "all":
				config.ExpandPromptShellCommands.PrimaryPrompts = true
				config.ExpandPromptShellCommands.SubagentPrompts = true
				config.ExpandPromptShellCommands.SkillPrompts = true
			case "primary":
				config.ExpandPromptShellCommands.PrimaryPrompts = true
			case "subagent":
				config.ExpandPromptShellCommands.SubagentPrompts = true
			case "skill":
				config.ExpandPromptShellCommands.SkillPrompts = true
			case "input":
				config.ExpandPromptShellCommands.InputPrompts = true
			default:
				return Config{}, fmt.Errorf("ROCKETCODE_EXPAND_PROMPT_SHELL_COMMANDS contains unknown value %q: expected primary, subagent, skill, input, or all", token)
			}
		}
	}

	if value := os.Getenv("ROCKETCODE_COMPACT_THRESHOLD"); value != "" {
		threshold, err := strconv.ParseInt(value, 10, 64)
		if err != nil || threshold <= 0 {
			return Config{}, errors.New("ROCKETCODE_COMPACT_THRESHOLD must be a positive integer")
		}

		config.CompactThreshold = threshold
	}

	config.CompactionSteering = os.Getenv("ROCKETCODE_COMPACTION_STEERING")

	return config, nil
}

func standaloneDefaultConfig() Config {
	return Config{
		Model:            openai.ChatModelGPT5_4,
		ReasoningEffort:  shared.ReasoningEffort("high"),
		CompactThreshold: 200000,
		ShellOutputDir:   filepath.Join(".tmp", "shell-outputs"),
		CustomTools: []Tool{{
			Name:        "current_time",
			Description: "Tell the current time anywhere in the world.",
			Call: func(context.Context, json.RawMessage, chan<- ChatResponse) (ToolResult, error) {
				return TextToolResult(time.Now().String()), nil
			},
		}},
	}
}

// LoadWorkspaceDefinitions loads top-level workspace agents and recursive skills.
func LoadWorkspaceDefinitions(root *os.Root) (Agents, Skills, func(), error) {
	agentsRoot, agentsFS, err := fsFromRoot(root, "agents")
	if err != nil {
		return Agents{}, Skills{}, func() {}, err
	}

	skillsRoot, skillsFS, err := fsFromRoot(root, "skills")
	if err != nil {
		if agentsRoot != nil {
			_ = agentsRoot.Close()
		}

		return Agents{}, Skills{}, func() {}, err
	}

	cleanup := func() {
		if skillsRoot != nil {
			_ = skillsRoot.Close()
		}

		if agentsRoot != nil {
			_ = agentsRoot.Close()
		}
	}

	agentResult := AgentLoadResult{Agents: Agents{Items: map[string]Agent{}}}
	if agentsFS != nil {
		agentResult = LoadAgents(agentsFS)
	}

	skillResult := SkillLoadResult{Skills: Skills{Items: map[string]Skill{}}}

	if skillsRoot != nil {
		skillResult = LoadSkills(skillsFS, skillsRoot.Name())
	}

	return agentResult.Agents, skillResult.Skills, cleanup, nil
}

func fsFromRoot(root *os.Root, name string) (*os.Root, fs.FS, error) {
	child, err := root.OpenRoot(name)
	if err == nil {
		fsys := child.FS()

		return child, fsys, nil
	}

	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}

	return nil, nil, fmt.Errorf("open %s root: %w", name, err)
}

// SplitPromptAttachmentTokens removes @attach:path tokens and returns the paths they name.
func SplitPromptAttachmentTokens(text string) (prompt string, files []string, err error) {
	parts := strings.Fields(text)
	files = []string{}
	kept := make([]string, 0, len(parts))

	for _, part := range parts {
		path, ok := strings.CutPrefix(part, "@attach:")
		if ok {
			if path == "" {
				return "", nil, errors.New("@attach requires a file path")
			}

			files = append(files, path)

			continue
		}

		kept = append(kept, part)
	}

	return strings.Join(kept, " "), files, nil
}

// PromptAttachments loads prompt attachments from workspace-local files.
func PromptAttachments(root *os.Root, cwd string, files []string) ([]Attachment, error) {
	attachments := make([]Attachment, 0, len(files))
	for _, file := range files {
		name := file
		if !filepath.IsAbs(name) {
			name = filepath.Join(cwd, name)
		}

		abs, err := filepath.Abs(name)
		if err != nil {
			return nil, fmt.Errorf("resolve attachment %q: %w", file, err)
		}

		rel, err := filepath.Rel(cwd, abs)
		if err != nil || !filepath.IsLocal(rel) {
			return nil, fmt.Errorf("attachment %q is outside the workspace", file)
		}

		data, err := root.ReadFile(rel)
		if err != nil {
			return nil, fmt.Errorf("read attachment %q: %w", file, err)
		}

		attachment, err := attachmentFromBytes(filepath.Base(rel), sniffAttachmentMIME(data, rel), data)
		if err != nil {
			return nil, fmt.Errorf("attach %q: %w", file, err)
		}

		attachments = append(attachments, attachment)
	}

	return attachments, nil
}
