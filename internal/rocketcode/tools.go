package rocketcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

type toolFactory struct {
	client                     responsesAPI
	anthropicClient            *anthropic.Client
	systemPrompt               string
	modelRef                   modelRef
	reasoningEffort            shared.ReasoningEffort
	compactThreshold           int64
	compactionSteering         string
	parallelToolCalls          int
	diagnostics                bool
	experimentalStrongerSkills bool
	expandPromptShellCommands  PromptShellCommandExpansion
	promptExpansion            promptExpansionEnvironment
	agent                      *Agent
	recursionRemaining         *int
	agents                     Agents
	skills                     Skills
	baseTools                  map[string]looperTool
	shellOutput                shellOutputConfig
	interAgentFilter           *interAgentFilter
}

type readToolParams struct {
	FilePath string `json:"filePath"`
	Filename string `json:"filename"`
	Offset   int    `json:"offset"`
}

type applyPatchToolParams struct {
	PatchText string `json:"patchText"`
	Patch     string `json:"patch"`
}

type globToolParams struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

type grepToolParams struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Include string `json:"include"`
}

type webFetchToolParams struct {
	URL     string `json:"url"`
	Format  string `json:"format"`
	Timeout int    `json:"timeout"`
}

func newSandboxedTools(root *os.Root, shellOutput shellOutputConfig, shellEnv []string, useSandbox bool) map[string]looperTool {
	sfs := &sandboxedFileSystem{mu: sync.Mutex{}, root: root}
	sss := newSandboxedShellSystem(root, &shellOutput, shellEnv, useSandbox)

	return makeSandboxedTools(sfs, sss)
}

func (f *toolFactory) toolsFor(agent *Agent) map[string]looperTool {
	tools := make(map[string]looperTool, len(f.baseTools))
	maps.Copy(tools, f.baseTools)

	if agent != nil {
		scopedAgent := *agent
		scopedAgent.Permission = f.shellOutput.effectivePermissions(scopedAgent.Permission)
		agent = &scopedAgent
	}

	scoped := *f
	scoped.agent = agent
	tools["find_skills"] = scoped.findSkillsTool()
	tools["skill"] = scoped.skillTool()

	tools["task"] = scoped.taskTool()
	if scoped.recursionRemaining != nil && *scoped.recursionRemaining == 0 {
		delete(tools, "task")
	}

	for name := range tools {
		tool := tools[name]
		if !toolVisible(agent, name, &tool) {
			delete(tools, name)
		}
	}

	if len(scoped.availableSkillSubjects()) == 0 {
		delete(tools, "find_skills")
	}

	return tools
}

func toolVisible(agent *Agent, name string, tool *looperTool) bool {
	permission := tool.Permission
	if permission == "" {
		permission = name
	}

	if len(tool.VisibilitySubjects) > 0 {
		for _, subject := range tool.VisibilitySubjects {
			if agent != nil && agent.Permission.evaluate(permission, subject).Action == permissionAllow {
				return true
			}
		}

		return false
	}

	if agent == nil {
		return false
	}

	return agent.Permission.hasAllowRuleForPermission(permission)
}

func makeSandboxedTools(sfs *sandboxedFileSystem, sss *sandboxedShellSystem) map[string]looperTool {
	return map[string]looperTool{
		"websearch": webSearchTool(),
		"read": {
			Definition: *functionTool("read", "Read a file from the workspace", map[string]any{
				"filePath": map[string]any{"type": "string"},
				"offset":   map[string]any{"type": "integer"},
			}),
			Permission: "read",
			Subjects: func(raw json.RawMessage) ([]string, error) {
				var params readToolParams
				if err := decodeToolParams(raw, &params); err != nil {
					return nil, err
				}

				return []string{sfs.readPermissionSubject(readToolPath(params))}, nil
			},
			Call: func(_ context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
				var params readToolParams
				if err := decodeToolParams(raw, &params); err != nil {
					return ToolResult{}, err
				}

				return sfs.ReadResult(readToolPath(params), max(params.Offset, 1)), nil
			},
		},
		"apply_patch": {
			Definition: *functionTool("apply_patch", "Apply a patch to files in the workspace", map[string]any{
				"patchText": map[string]any{"type": "string"},
			}),
			Permission: "edit",
			Subjects: func(raw json.RawMessage) ([]string, error) {
				var params applyPatchToolParams
				if err := decodeToolParams(raw, &params); err != nil {
					return nil, err
				}

				preview, errText := previewApplyPatch(sfs, applyPatchText(params))
				if errText != "" {
					return nil, errors.New(errText)
				}

				subjects := make([]string, 0, len(preview.changes))
				for _, change := range preview.changes {
					target := change.path
					if change.movePath != "" {
						target = change.movePath
					}

					subjects = append(subjects, rootedPathSubject(target))
				}

				return subjects, nil
			},
			Call: func(_ context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
				var params applyPatchToolParams
				if err := decodeToolParams(raw, &params); err != nil {
					return ToolResult{}, err
				}

				return TextToolResult(sfs.ApplyPatch(applyPatchText(params))), nil
			},
		},
		"glob": {
			Definition: *functionTool("glob", "Find files in the workspace by glob pattern", map[string]any{
				"pattern": map[string]any{"type": "string"},
				"path":    map[string]any{"type": "string"},
			}),
			Permission: "glob",
			Subjects: func(raw json.RawMessage) ([]string, error) {
				var params globToolParams
				if err := decodeToolParams(raw, &params); err != nil {
					return nil, err
				}

				return []string{params.Pattern}, nil
			},
			Call: func(ctx context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
				var params globToolParams
				if err := decodeToolParams(raw, &params); err != nil {
					return ToolResult{}, err
				}

				return TextToolResult(sfs.Glob(ctx, params.Pattern, params.Path)), nil
			},
		},
		"grep": {
			Definition: *functionTool("grep", "Search file contents in the workspace", map[string]any{
				"pattern": map[string]any{"type": "string"},
				"path":    map[string]any{"type": "string"},
				"include": map[string]any{"type": "string"},
			}),
			Permission: "grep",
			Subjects: func(raw json.RawMessage) ([]string, error) {
				var params grepToolParams
				if err := decodeToolParams(raw, &params); err != nil {
					return nil, err
				}

				return []string{params.Pattern}, nil
			},
			Call: func(ctx context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
				var params grepToolParams
				if err := decodeToolParams(raw, &params); err != nil {
					return ToolResult{}, err
				}

				return TextToolResult(sfs.Grep(ctx, params.Pattern, params.Path, params.Include)), nil
			},
		},
		"webfetch": {
			Definition: *functionTool("webfetch", webFetchDescription(), map[string]any{
				"url":     map[string]any{"type": "string"},
				"format":  map[string]any{"type": "string", "enum": []string{"text", "markdown", "html"}},
				"timeout": map[string]any{"type": "integer"},
			}),
			Permission: "webfetch",
			Subjects: func(raw json.RawMessage) ([]string, error) {
				var params webFetchToolParams
				if err := decodeToolParams(raw, &params); err != nil {
					return nil, err
				}

				return []string{params.URL}, nil
			},
			Call: func(ctx context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
				var params webFetchToolParams
				if err := decodeToolParams(raw, &params); err != nil {
					return ToolResult{}, err
				}

				return webFetch(ctx, params)
			},
		},
		"bash": {
			Definition: *functionTool("bash", "Run a shell command in the workspace", map[string]any{
				"command":     map[string]any{"type": "string"},
				"timeout":     map[string]any{"type": "integer"},
				"workdir":     map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
			}),
			Permission: "bash",
			Subjects: func(raw json.RawMessage) ([]string, error) {
				var params bashParams
				if err := decodeToolParams(raw, &params); err != nil {
					return nil, err
				}

				return BashPermissionSubjects(params.Command), nil
			},
			Call: func(ctx context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
				var params bashParams
				if err := decodeToolParams(raw, &params); err != nil {
					return ToolResult{}, err
				}

				return TextToolResult(sss.Bash(ctx, params)), nil
			},
		},
	}
}

func webSearchTool() looperTool {
	return looperTool{Hosted: responses.ToolUnionParam{OfWebSearch: &responses.WebSearchToolParam{Type: responses.WebSearchToolTypeWebSearch}}, Permission: "websearch"}
}

func (f *toolFactory) findSkillsTool() looperTool {
	type params struct {
		Query string `json:"query"`
	}

	return looperTool{
		Definition: *functionTool("find_skills", strings.Join([]string{
			"Search available skills by name, description, and content.",
			"Use this tool before the skill tool whenever a task may benefit from specialized instructions and the right skill is not already obvious from the system prompt.",
			"The results include skill names and descriptions. Call the skill tool with one of the returned names to load its full instructions.",
		}, "\n"), map[string]any{
			"query": map[string]any{"type": "string"},
		}),
		Permission: "skill",
		Subjects: func(json.RawMessage) ([]string, error) {
			return f.availableSkillSubjects(), nil
		},
		Call: func(_ context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
			var input params
			if err := decodeToolParams(raw, &input); err != nil {
				return ToolResult{}, err
			}

			return TextToolResult(f.skills.FindAvailable(input.Query, f.agent)), nil
		},
	}
}

func (f *toolFactory) availableSkillSubjects() []string {
	if len(f.skills.Items) == 0 {
		return nil
	}

	subjects := make([]string, 0, len(f.skills.Items))
	for _, skill := range availableSkills(f.skills.Items, f.agent) {
		subjects = append(subjects, skill.Name)
	}

	return subjects
}

type skillToolParams struct {
	Name string `json:"name"`
}

func (f *toolFactory) skillTool() looperTool {
	return looperTool{
		Definition: *functionTool("skill", f.skillDescription(), map[string]any{
			"name": map[string]any{"type": "string"},
		}),
		Permission: "skill",
		Subjects: func(raw json.RawMessage) ([]string, error) {
			var input skillToolParams
			if err := decodeToolParams(raw, &input); err != nil {
				return nil, err
			}

			return []string{input.Name}, nil
		},
		Call: func(ctx context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
			_, output, err := f.renderSkillToolOutput(ctx, raw, false)
			if err != nil {
				return ToolResult{}, err
			}

			return TextToolResult(output), nil
		},
		CallReplay: func(ctx context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, []responses.ResponseInputItemUnionParam, error) {
			input, output, err := f.renderSkillToolOutput(ctx, raw, f.experimentalStrongerSkills)
			if err != nil {
				return ToolResult{}, nil, err
			}

			if !f.experimentalStrongerSkills {
				return TextToolResult(output), nil, nil
			}

			message := inputMessageParam(responses.EasyInputMessageRoleDeveloper, easyInputStringContent(output))

			return TextToolResult("skill " + input.Name + " loaded"), []responses.ResponseInputItemUnionParam{message}, nil
		},
	}
}

func (f *toolFactory) renderSkillToolOutput(ctx context.Context, raw json.RawMessage, includeMetadata bool) (skillToolParams, string, error) {
	var input skillToolParams
	if err := decodeToolParams(raw, &input); err != nil {
		return skillToolParams{}, "", err
	}

	skill, ok := f.skills.Items[input.Name]
	if !ok {
		return skillToolParams{}, "", fmt.Errorf("skill %q not found. Available skills: %s", input.Name, strings.Join(slices.Sorted(maps.Keys(f.skills.Items)), ", "))
	}

	dir := path.Dir(skill.Location)

	files, err := f.skills.skillFiles(dir)
	if err != nil {
		return skillToolParams{}, "", err
	}

	lines := []string{
		fmt.Sprintf(`<skill_content name=%q>`, skill.Name),
		"# skill: " + skill.Name,
		"",
	}

	if includeMetadata {
		metadata, err := json.MarshalIndent(skill.Metadata, "", "  ")
		if err != nil {
			return skillToolParams{}, "", fmt.Errorf("render skill metadata: %w", err)
		}

		lines = append(lines,
			"<skill_metadata>",
			"name: "+skill.Name,
			"description: "+skill.Description,
			"license: "+skill.License,
			"compatibility: "+skill.Compatibility,
			"location: "+skill.Location,
			"metadata: "+string(metadata),
			"</skill_metadata>",
			"",
		)
	}

	lines = append(lines,
		strings.TrimSpace(skill.Content),
		"",
		"Base directory for this skill: "+fileURL(filepath.Join(f.skills.Root, filepath.FromSlash(dir))),
		"Relative paths in this skill (e.g., scripts/, reference/) are relative to this base directory.",
		"Note: file list is sampled.",
		"",
		"<skill_files>",
		strings.Join(files, "\n"),
		"</skill_files>",
		"</skill_content>",
	)

	output := strings.Join(lines, "\n")

	if f.expandPromptShellCommands.SkillPrompts {
		output = f.promptExpansion.expandShellCommands(ctx, output)
	}

	return input, output, nil
}

func (f *toolFactory) skillDescription() string {
	list := availableSkills(f.skills.Items, f.agent)
	if len(list) == 0 {
		return strings.Join([]string{
			"Load a specialized skill when the task at hand matches one of the skills listed in the system prompt.",
			"",
			"Use this tool to inject the skill's instructions and resources into current conversation. The output may contain detailed workflow guidance as well as references to scripts, files, etc in the same directory as the skill.",
			"",
			"The skill name must match one of the skills listed in your system prompt.",
			"",
			"No skills are currently available.",
		}, "\n")
	}

	return strings.Join([]string{
		"Load a specialized skill when the task at hand matches one of the skills listed in the system prompt.",
		"",
		"Use this tool to inject the skill's instructions and resources into current conversation. The output may contain detailed workflow guidance as well as references to scripts, files, etc in the same directory as the skill.",
		"",
		"The skill name must match one of the skills listed in your system prompt.",
		"",
		"The following skills provide specialized sets of instructions for particular tasks.",
		"Invoke this tool to load a skill when a task matches one of the available skills listed below:",
		"",
		formatAvailableSkills(list),
	}, "\n")
}

func functionTool(name, description string, properties map[string]any) *responses.FunctionToolParam {
	// OpenAI strict function schemas require every property key to appear in
	// required. Tools still apply defaults after JSON decoding when callers pass
	// zero values.
	required := slices.Sorted(maps.Keys(properties))

	parameters := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
		"required":             required,
	}

	var tool responses.FunctionToolParam

	tool.Name = name
	tool.Description = openai.String(description)
	tool.Parameters = parameters
	tool.Strict = openai.Bool(true)

	return &tool
}

func decodeToolParams[T any](raw json.RawMessage, params *T) error {
	if err := json.Unmarshal(raw, params); err != nil {
		return fmt.Errorf("decode tool params: %w", err)
	}

	return nil
}

func readToolPath(params readToolParams) string {
	if params.FilePath != "" {
		return params.FilePath
	}

	return params.Filename
}

func applyPatchText(params applyPatchToolParams) string {
	if params.PatchText != "" {
		return params.PatchText
	}

	return params.Patch
}
