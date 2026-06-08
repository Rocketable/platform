//nolint:exhaustruct,gocritic,ireturn // Tool definitions use sparse SDK structs and generic JSON decoding helpers.
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

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

type toolFactory struct {
	client                     responsesAPI
	systemPrompt               string
	model                      shared.ResponsesModel
	reasoningEffort            shared.ReasoningEffort
	compactThreshold           int64
	compactionSteering         string
	parallelToolCalls          int
	diagnostics                bool
	experimentalStrongerSkills bool
	expandPromptShellCommands  PromptShellCommandExpansion
	promptExpansion            promptExpansionEnvironment
	agent                      *Agent
	agents                     Agents
	skills                     Skills
	baseTools                  map[string]looperTool
	shellOutput                shellOutputConfig
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
	sfs := &sandboxedFileSystem{root: root}
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

	for name, tool := range tools {
		if !toolVisible(agent, name, tool) {
			delete(tools, name)
		}
	}

	if len(scoped.availableSkillSubjects()) == 0 {
		delete(tools, "find_skills")
	}

	return tools
}

func toolVisible(agent *Agent, name string, tool looperTool) bool {
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
	if sfs == nil || sss == nil {
		return map[string]looperTool{
			"read":      {Definition: functionTool("read", "Read a file from the workspace", map[string]any{"filePath": map[string]any{"type": "string"}}), Permission: "read"},
			"bash":      {Definition: functionTool("bash", "Run a shell command in the workspace", map[string]any{"command": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}}), Permission: "bash"},
			"websearch": webSearchTool(),
		}
	}

	return map[string]looperTool{
		"websearch": webSearchTool(),
		"read": {
			Definition: functionTool("read", "Read a file from the workspace", map[string]any{
				"filePath": map[string]any{"type": "string"},
				"offset":   map[string]any{"type": "integer"},
			}),
			Permission: "read",
			Subjects: func(raw json.RawMessage) ([]string, error) {
				params, err := decodeToolParams[readToolParams](raw)
				if err != nil {
					return nil, err
				}

				return []string{sfs.readPermissionSubject(readToolPath(params))}, nil
			},
			Call: func(_ context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
				params, err := decodeToolParams[readToolParams](raw)
				if err != nil {
					return ToolResult{}, err
				}

				return sfs.ReadResult(readToolPath(params), max(params.Offset, 1)), nil
			},
		},
		"apply_patch": {
			Definition: functionTool("apply_patch", "Apply a patch to files in the workspace", map[string]any{
				"patchText": map[string]any{"type": "string"},
			}),
			Permission: "edit",
			Subjects: func(raw json.RawMessage) ([]string, error) {
				params, err := decodeToolParams[applyPatchToolParams](raw)
				if err != nil {
					return nil, err
				}

				preview, errText := sfs.previewApplyPatch(applyPatchText(params))
				if errText != "" {
					return nil, errors.New(errText)
				}

				subjects := make([]string, 0, len(preview.files))
				for _, file := range preview.files {
					subjects = append(subjects, rootedPathSubject(file.RelativePath))
				}

				return subjects, nil
			},
			Call: func(_ context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
				params, err := decodeToolParams[applyPatchToolParams](raw)
				if err != nil {
					return ToolResult{}, err
				}

				return textToolResult(sfs.ApplyPatch(applyPatchText(params))), nil
			},
		},
		"glob": {
			Definition: functionTool("glob", "Find files in the workspace by glob pattern", map[string]any{
				"pattern": map[string]any{"type": "string"},
				"path":    map[string]any{"type": "string"},
			}),
			Permission: "glob",
			Subjects: func(raw json.RawMessage) ([]string, error) {
				params, err := decodeToolParams[globToolParams](raw)
				if err != nil {
					return nil, err
				}

				return []string{params.Pattern}, nil
			},
			Call: func(ctx context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
				params, err := decodeToolParams[globToolParams](raw)
				if err != nil {
					return ToolResult{}, err
				}

				return textToolResult(sfs.Glob(ctx, params.Pattern, params.Path)), nil
			},
		},
		"grep": {
			Definition: functionTool("grep", "Search file contents in the workspace", map[string]any{
				"pattern": map[string]any{"type": "string"},
				"path":    map[string]any{"type": "string"},
				"include": map[string]any{"type": "string"},
			}),
			Permission: "grep",
			Subjects: func(raw json.RawMessage) ([]string, error) {
				params, err := decodeToolParams[grepToolParams](raw)
				if err != nil {
					return nil, err
				}

				return []string{params.Pattern}, nil
			},
			Call: func(ctx context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
				params, err := decodeToolParams[grepToolParams](raw)
				if err != nil {
					return ToolResult{}, err
				}

				return textToolResult(sfs.Grep(ctx, params.Pattern, params.Path, params.Include)), nil
			},
		},
		"webfetch": {
			Definition: functionTool("webfetch", webFetchDescription(), map[string]any{
				"url":     map[string]any{"type": "string"},
				"format":  map[string]any{"type": "string", "enum": []string{"text", "markdown", "html"}},
				"timeout": map[string]any{"type": "integer"},
			}),
			Permission: "webfetch",
			Subjects: func(raw json.RawMessage) ([]string, error) {
				params, err := decodeToolParams[webFetchToolParams](raw)
				if err != nil {
					return nil, err
				}

				return []string{params.URL}, nil
			},
			Call: func(ctx context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
				params, err := decodeToolParams[webFetchToolParams](raw)
				if err != nil {
					return ToolResult{}, err
				}

				return webFetch(ctx, params)
			},
		},
		"bash": {
			Definition: functionTool("bash", "Run a shell command in the workspace", map[string]any{
				"command":     map[string]any{"type": "string"},
				"timeout":     map[string]any{"type": "integer"},
				"workdir":     map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
			}),
			Permission: "bash",
			Subjects: func(raw json.RawMessage) ([]string, error) {
				params, err := decodeToolParams[bashParams](raw)
				if err != nil {
					return nil, err
				}

				return bashPermissionSubjects(params.Command), nil
			},
			Call: func(ctx context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
				params, err := decodeToolParams[bashParams](raw)
				if err != nil {
					return ToolResult{}, err
				}

				return textToolResult(sss.Bash(ctx, params)), nil
			},
		},
	}
}

func webSearchTool() looperTool {
	return looperTool{
		Hosted:     responses.ToolUnionParam{OfWebSearch: &responses.WebSearchToolParam{Type: responses.WebSearchToolTypeWebSearch}},
		Permission: "websearch",
		Subjects: func(json.RawMessage) ([]string, error) {
			return []string{"*"}, nil
		},
	}
}

func (f *toolFactory) findSkillsTool() looperTool {
	type params struct {
		Query string `json:"query"`
	}

	return looperTool{
		Definition: functionTool("find_skills", strings.Join([]string{
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
			input, err := decodeToolParams[params](raw)
			if err != nil {
				return ToolResult{}, err
			}

			return textToolResult(f.skills.FindAvailable(input.Query, f.agent)), nil
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
		Definition: functionTool("skill", f.skillDescription(), map[string]any{
			"name": map[string]any{"type": "string"},
		}),
		Permission: "skill",
		Subjects: func(raw json.RawMessage) ([]string, error) {
			input, err := decodeToolParams[skillToolParams](raw)
			if err != nil {
				return nil, err
			}

			return []string{input.Name}, nil
		},
		Call: func(ctx context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
			_, output, err := f.renderSkillToolOutput(ctx, raw, false)
			if err != nil {
				return ToolResult{}, err
			}

			return textToolResult(output), nil
		},
		CallReplay: func(ctx context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, []responses.ResponseInputItemUnionParam, error) {
			input, output, err := f.renderSkillToolOutput(ctx, raw, f.experimentalStrongerSkills)
			if err != nil {
				return ToolResult{}, nil, err
			}

			if !f.experimentalStrongerSkills {
				return textToolResult(output), nil, nil
			}

			message := responses.ResponseInputItemUnionParam{OfMessage: &responses.EasyInputMessageParam{
				Role:    responses.EasyInputMessageRoleDeveloper,
				Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(output)},
				Type:    "message",
			}}

			return textToolResult("skill " + input.Name + " loaded"), []responses.ResponseInputItemUnionParam{message}, nil
		},
	}
}

func (f *toolFactory) renderSkillToolOutput(ctx context.Context, raw json.RawMessage, includeMetadata bool) (skillToolParams, string, error) {
	input, err := decodeToolParams[skillToolParams](raw)
	if err != nil {
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
		formatAvailableSkills(list, f.skills.Root, false),
	}, "\n")
}

func functionTool(name, description string, properties map[string]any) responses.FunctionToolParam {
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

	return responses.FunctionToolParam{Name: name, Description: openai.String(description), Parameters: parameters, Strict: openai.Bool(true)}
}

func decodeToolParams[T any](raw json.RawMessage) (T, error) {
	var params T
	if err := json.Unmarshal(raw, &params); err != nil {
		return params, fmt.Errorf("decode tool params: %w", err)
	}

	return params, nil
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

func parseAgentModel(model string, fallback shared.ResponsesModel) shared.ResponsesModel {
	if model != "" {
		return model
	}

	return fallback
}
