package harness

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
	"github.com/Rocketable/platform/internal/rocketclaw/mcpclient"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"go.starlark.net/starlark"
)

type toolFactory struct {
	resolver                  ModelResolver
	workspaceInstructions     string
	autoApproverModel         string
	autoApproverDecisionModel string
	autoApproverMinConfidence float64
	deciders                  map[string]Decider
	parallelToolCalls         int
	expandPromptShellCommands PromptShellCommandExpansion
	promptExpansion           promptExpansionEnvironment
	agent                     *Agent
	recursionRemaining        *int
	agents                    Agents
	skills                    Skills
	baseTools                 map[string]looperTool
	shellSystem               *sandboxedShellSystem
	childContext              []SessionEntry
	originHeader              string
	shellTemp                 shellTempConfig
	spillRel                  string
	retainedRel               string
	observability             ObservabilityConfig
	childSessions             ChildSessions
	backgroundJobs            BackgroundJobs
	childKey                  ChildKey

	inPermissionReview bool

	mcpRegistry mcpclient.Registry
}

type readToolParams struct {
	FilePath string `json:"filePath"`
	Offset   int    `json:"offset"`
}

type applyPatchToolParams struct {
	PatchText string `json:"patchText"`
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
	URL           string `json:"url"`
	Format        string `json:"format"`
	TimeoutSecond int    `json:"timeout_s"`
}

func newSandboxedTools(root *os.Root, spillRel string, shellTemp shellTempConfig, shellEnv []string, rt *sandbox.Runtime, turnSecrets TurnSecrets) (map[string]looperTool, *sandboxedShellSystem) {
	sfs := &sandboxedFileSystem{mu: sync.Mutex{}, root: root, spillRel: spillRel}
	sss := newSandboxedShellSystem(root, &shellTemp, shellEnv, rt, turnSecrets)
	sss.spillRel = spillRel

	return makeSandboxedTools(sfs, sss), sss
}

func (f *toolFactory) assembleTools(agent *Agent) (model, codeHosts map[string]looperTool) {
	tools := make(map[string]looperTool, len(f.baseTools)+4)
	maps.Copy(tools, f.baseTools)

	scoped := *f

	scopedAgent := *agent
	scopedAgent.Permission = f.shellTemp.effectivePermissions(scopedAgent.Permission)
	agent = &scopedAgent
	scoped.promptExpansion = f.promptExpansion.forAgent(agent)

	if agent.CustomShell.Path != "" || agent.CustomShell.Emulated {
		tools["bash"] = bashTool(f.shellSystem, &agent.CustomShell)
	}

	if agent.Mode == AgentModeEasy {
		easy := makeSandboxedTools(&easyFiles{shell: agent.CustomShell}, f.shellSystem)
		for _, name := range []string{"read", "apply_patch", "glob", "grep"} {
			tools[name] = easy[name]
		}

		tools["webfetch"] = webFetchTool(easyWebFetchClient(agent.NetworkTargets))
	}

	scoped.agent = agent
	tools["find_skills"] = scoped.findSkillsTool()
	tools["skill"] = scoped.skillTool()

	tools["task"] = scoped.taskTool()
	if scoped.recursionRemaining != nil && *scoped.recursionRemaining == 0 {
		delete(tools, "task")
	}

	if agent.Tools != nil {
		maps.DeleteFunc(tools, func(name string, _ looperTool) bool { return !slices.Contains(agent.Tools, name) })
	}

	codeHosts = make(map[string]looperTool, 6)

	for name := range tools {
		tool := tools[name]
		if !toolVisible(agent, name, &tool) {
			delete(tools, name)

			continue
		}

		if tool.codeModeOnly || slices.Contains([]string{"read", "apply_patch", "glob", "grep", "webfetch", "bash"}, name) {
			codeHosts[name] = tool
			delete(tools, name)
		}
	}

	if tool, ok := codeHosts["decision"]; ok {
		var models []string

		for _, bucket := range agent.Permission.Buckets {
			for _, rule := range bucket.Rules {
				if bucket.Name == "decision" && (rule.Action == permissionAllow || rule.Action == permissionAuto) {
					models = append(models, rule.Pattern)
				}
			}
		}

		if agent.Permission.Sudo {
			for _, provider := range slices.Sorted(maps.Keys(f.deciders)) {
				models = append(models, provider+"/*")
			}
		}

		tool.Definition.Description = openai.String(decisionToolDescription(models))
		codeHosts["decision"] = tool
	}

	maps.Copy(tools, scoped.mcpToolsFor(agent, codeHosts))

	if len(scoped.availableSkillSubjects()) == 0 {
		delete(tools, "find_skills")
	}

	return tools, codeHosts
}

func (f *toolFactory) newLooper(agent *Agent, systemPrompt string) (*looper, error) {
	client, origin, err := resolveModel(f.resolver, agent.Model)
	if err != nil {
		return nil, err
	}

	if agent.Tools != nil {
		restricted := *agent
		restricted.Permission.Buckets = append(slices.DeleteFunc(slices.Clone(agent.Permission.Buckets), func(bucket PermissionBucket) bool { return bucket.Name == mcpPermissionBucket }),
			PermissionBucket{Name: mcpPermissionBucket, Rules: []PermissionRule{{Pattern: "*", Action: PermissionDeny}}})
		agent = &restricted
	}

	modelTools, codeHosts := f.assembleTools(agent)

	mcpServers := visibleMCPServers(agent.Permission, f.mcpRegistry.Names())

	loop := &looper{
		agent:              *agent,
		ProviderOrigin:     origin,
		Client:             responseServiceClient{service: &client.Responses, doer: &responsesWebsocketDoer{}},
		SystemPrompt:       withCodeModeSystemPrompt(composeSystemPromptWithSkills(systemPrompt, f.skills, agent), modelTools, codeHosts, mcpServers),
		DisplayModel:       origin.displayModel(),
		ReasoningEffort:    shared.ReasoningEffort(agent.ReasoningEffort),
		Verbosity:          agent.Verbosity,
		CompactThreshold:   cmp.Or(origin.CompactThreshold, defaultCompactThreshold),
		ParallelToolCalls:  f.parallelToolCalls,
		ResponseFormat:     agentOutputResponseFormat(agent.OutputSchema),
		Permissions:        agent.Permission,
		Tools:              modelTools,
		CodeModeHosts:      codeHosts,
		PermissionReviewer: f,
		InPermissionReview: f.inPermissionReview,
		Observability:      f.observability,
		Journal:            InertJournal{},
		SteerDrain:         InertSteerDrain{},
		promptExpansion:    f.promptExpansion,
		spillRel:           f.spillRel,
	}
	loop.promptExpansion.customShell = agent.CustomShell

	return loop, nil
}

func toolVisible(agent *Agent, name string, tool *looperTool) bool {
	permission := cmp.Or(tool.Permission, name)

	if len(tool.VisibilitySubjects) > 0 {
		for _, subject := range tool.VisibilitySubjects {
			if slices.Contains([]PermissionAction{permissionAllow, permissionAuto}, agent.Permission.evaluate(permission, subject).Action) {
				return true
			}
		}

		return false
	}

	return agent.Permission.hasRuleForPermission(permission, permissionAllow, permissionAuto)
}

type fileSystem interface {
	Read(ctx context.Context, filename string, offset int) (starlark.Value, error)
	readPermissionSubject(name string) string
	previewApplyPatch(patchText string) (applyPatchPreview, string)
	ApplyPatch(patchText string) (starlark.Value, error)
	Glob(ctx context.Context, pattern, path string) (starlark.Value, error)
	Grep(ctx context.Context, pattern, searchPath, include string) (starlark.Value, error)
}

func makeSandboxedTools(sfs fileSystem, sss *sandboxedShellSystem) map[string]looperTool {
	return map[string]looperTool{
		"websearch": webSearchTool(),
		"read": paramsTool(functionTool("read", "Read a file from the workspace", map[string]any{
			"filePath": map[string]any{"type": "string"},
			"offset":   map[string]any{"type": "integer"},
		}), "read", func(params readToolParams) ([]string, error) {
			return []string{sfs.readPermissionSubject(params.FilePath)}, nil
		}, func(ctx context.Context, params readToolParams) (ToolResult, error) {
			return typedToolResult(sfs.Read(ctx, params.FilePath, max(params.Offset, 1)))
		}),
		"apply_patch": paramsTool(functionTool("apply_patch", "Apply a patch to files in the workspace", map[string]any{
			"patchText": map[string]any{"type": "string"},
		}), "edit", func(params applyPatchToolParams) ([]string, error) {
			preview, errText := sfs.previewApplyPatch(params.PatchText)
			if errText != "" {
				return nil, errors.New(errText)
			}

			subjects := make([]string, 0, len(preview.changes))
			for _, change := range preview.changes {
				subjects = append(subjects, sfs.readPermissionSubject(cmp.Or(change.movePath, change.path)))
			}

			return subjects, nil
		}, func(_ context.Context, params applyPatchToolParams) (ToolResult, error) {
			return typedToolResult(sfs.ApplyPatch(params.PatchText))
		}),
		"glob": paramsTool(functionTool("glob", "Find files in the workspace by glob pattern", map[string]any{
			"pattern": map[string]any{"type": "string"},
			"path":    map[string]any{"type": "string"},
		}), "glob", func(params globToolParams) ([]string, error) {
			return []string{sfs.readPermissionSubject(cmp.Or(params.Path, "."))}, nil
		}, func(ctx context.Context, params globToolParams) (ToolResult, error) {
			return typedToolResult(sfs.Glob(ctx, params.Pattern, params.Path))
		}),
		"grep": paramsTool(functionTool("grep", "Search file contents in the workspace", map[string]any{
			"pattern": map[string]any{"type": "string"},
			"path":    map[string]any{"type": "string"},
			"include": map[string]any{"type": "string"},
		}), "grep", func(params grepToolParams) ([]string, error) {
			return []string{params.Pattern}, nil
		}, func(ctx context.Context, params grepToolParams) (ToolResult, error) {
			return typedToolResult(sfs.Grep(ctx, params.Pattern, params.Path, params.Include))
		}),
		"webfetch": webFetchTool(webFetchClient(false)),
		"bash":     bashTool(sss, &CustomShell{}),
	}
}

func paramsTool[P any](definition *responses.FunctionToolParam, permission string, subjects func(P) ([]string, error), call func(context.Context, P) (ToolResult, error)) looperTool {
	return looperTool{
		Definition: *definition,
		Permission: permission,
		Subjects: func(raw json.RawMessage) ([]string, error) {
			var params P
			if err := decodeToolParams(raw, &params); err != nil {
				return nil, err
			}

			return subjects(params)
		},
		Call: func(ctx context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
			var params P
			if err := decodeToolParams(raw, &params); err != nil {
				return ToolResult{}, err
			}

			return call(ctx, params)
		},
	}
}

func webFetchTool(client *http.Client) looperTool {
	return paramsTool(functionTool("webfetch", webFetchDescription, map[string]any{
		"url":       map[string]any{"type": "string"},
		"format":    map[string]any{"type": "string", "enum": []string{"text", "markdown", "html"}},
		"timeout_s": map[string]any{"type": "integer"},
	}), "webfetch", func(params webFetchToolParams) ([]string, error) {
		rawURL, _, _ := strings.Cut(params.URL, "#")

		return []string{rawURL}, nil
	}, func(ctx context.Context, params webFetchToolParams) (ToolResult, error) {
		return webFetch(ctx, client, params)
	})
}

func bashTool(sss *sandboxedShellSystem, customShell *CustomShell) looperTool {
	description := "Run a shell script in the workspace. Every operation needs permission, including declarations, assignments, redirections, expansions, and nested commands. Whole-script rules support wildcards within the same operation tree; last matching rule wins per operation. Invalid syntax and unresolved executable names are denied. stdin= takes text or a file, fed to the command's standard input."
	if customShell.Emulated {
		description += "\nCommands run in an emulated bash shell, which may limit what they can reach."
	} else if customShell.Path != "" {
		description += "\nCommands run through this agent's shell program, " + customShell.Path + ", which may limit what they can reach."
	}

	return paramsTool(functionTool("bash", description, map[string]any{
		"command":     map[string]any{"type": "string"},
		"timeout_ms":  map[string]any{"type": "integer"},
		"workdir":     map[string]any{"type": "string"},
		"description": map[string]any{"type": "string"},
		"stdin":       map[string]any{"type": []string{"string", "object"}},
	}), "bash", func(params bashParams) ([]string, error) {
		return BashPermissionSubjects(params.Command), nil
	}, func(ctx context.Context, params bashParams) (ToolResult, error) {
		if params.Stdin.fileSHA256 != "" {
			file, ok := codemode.ArgFile(ctx, params.Stdin.fileSHA256)
			if !ok {
				return ToolResult{}, errBashStdin
			}

			params.Stdin.text = file.Content()
		}

		result := sss.Bash(ctx, customShell, &params)

		return ToolResult{Output: result.String(), Data: result}, nil
	})
}

func webSearchTool() looperTool {
	return looperTool{Hosted: responses.ToolUnionParam{OfWebSearch: &responses.WebSearchToolParam{Type: responses.WebSearchToolTypeWebSearch}}, Permission: "websearch"}
}

func (f *toolFactory) findSkillsTool() looperTool {
	type params struct {
		Query string `json:"query"`
	}

	return paramsTool(functionTool("find_skills", strings.Join([]string{
		"Search available skills by name, description, and content.",
		"Use this tool before the skill tool whenever a task may benefit from specialized instructions and the right skill is not already obvious from the system prompt.",
		"The results include skill names and descriptions. Call the skill tool with one of the returned names to load its full instructions.",
	}, "\n"), map[string]any{
		"query": map[string]any{"type": "string"},
	}), "skill", func(params) ([]string, error) {
		return f.availableSkillSubjects(), nil
	}, func(_ context.Context, input params) (ToolResult, error) {
		return TextToolResult(f.skills.FindAvailable(input.Query, f.agent)), nil
	})
}

func (f *toolFactory) availableSkillSubjects() []string {
	if len(f.skills.Items) == 0 {
		return nil
	}

	subjects := make([]string, 0, len(f.skills.Items))
	for _, skill := range f.skills.Available(f.agent) {
		subjects = append(subjects, skill.Name)
	}

	return subjects
}

type skillNotFoundError struct {
	name string
}

func (e skillNotFoundError) Error() string {
	return "Unable to load skill " + e.name
}

type skillToolParams struct {
	ID        string `json:"id"`
	Arguments string `json:"arguments,omitempty"`
	Direct    bool   `json:"direct,omitempty"`
}

func (f *toolFactory) skillTool() looperTool {
	return paramsTool(functionTool("skill", "Load a specialized skill's instructions and resources into the current conversation when the task at hand matches its description.\n\nThe skill ID must match an available skill or a skill explicitly referenced by the user.", map[string]any{
		"id": map[string]any{"type": "string", "description": "The ID of an available skill or a skill explicitly referenced by the user"},
	}), "skill", func(input skillToolParams) ([]string, error) {
		return []string{input.ID}, nil
	}, func(ctx context.Context, input skillToolParams) (ToolResult, error) {
		output, err := f.renderSkillToolOutput(ctx, input)

		return TextToolResult(output), err
	})
}

func (f *toolFactory) renderSkillToolOutput(ctx context.Context, input skillToolParams) (string, error) {
	skill, ok := f.skills.Items[input.ID]
	if !ok {
		return "", skillNotFoundError{name: input.ID}
	}

	dir := path.Dir(skill.Location)

	files, err := f.skills.skillFiles(skill.Location)
	if err != nil {
		return "", err
	}

	content := strings.TrimSpace(skill.Content)

	easy := f.agent.Mode == AgentModeEasy
	if easy && f.expandPromptShellCommands.SkillPrompts {
		content = f.promptExpansion.expandShellCommands(ctx, content)
	}

	if input.Direct {
		content = renderSkillArguments(content, input.Arguments)
	}

	lines := slices.Concat([]string{
		fmt.Sprintf(`<skill_content name=%q>`, skill.DisplayName),
		"# Skill: " + skill.DisplayName,
		"",
		content,
		"",
		"Base directory for this skill: " + filepath.Join(f.skills.Root, filepath.FromSlash(dir)),
		"Relative paths in this skill (e.g., scripts/, reference/) are relative to this base directory.",
		"Note: file list is sampled.",
		"",
		"<skill_files>",
	}, files, []string{"</skill_files>", "</skill_content>"})

	output := strings.Join(lines, "\n")

	if !easy && f.expandPromptShellCommands.SkillPrompts {
		output = f.promptExpansion.expandShellCommands(ctx, output)
	}

	return output, nil
}

var (
	skillArgumentPattern    = regexp.MustCompile(`"[^"]*"|'[^']*'|\S+`)
	skillPlaceholderPattern = regexp.MustCompile(`\$(ARGUMENTS|[1-9]\d*)`)
)

func renderSkillArguments(output, arguments string) string {
	tokens := skillPlaceholderPattern.FindAllString(output, -1)
	if len(tokens) == 0 {
		if arguments == "" {
			return output
		}

		return output + "\n\n" + arguments
	}

	positions := skillArgumentPattern.FindAllString(arguments, -1)
	for i, argument := range positions {
		if len(argument) >= 2 && (argument[0] == '"' || argument[0] == '\'') && argument[len(argument)-1] == argument[0] {
			positions[i] = argument[1 : len(argument)-1]
		}
	}

	indices := map[string]int{}
	highest := 0

	for _, token := range tokens {
		if token == "$ARGUMENTS" {
			continue
		}

		index, err := strconv.Atoi(token[1:])
		if err != nil {
			index = len(positions) + 1
		}

		indices[token] = index
		highest = max(highest, index)
	}

	return skillPlaceholderPattern.ReplaceAllStringFunc(output, func(token string) string {
		if token == "$ARGUMENTS" {
			return arguments
		}

		index := indices[token]
		if index > len(positions) {
			return ""
		}

		if index == highest {
			return strings.Join(positions[index-1:], " ")
		}

		return positions[index-1]
	})
}

func functionTool(name, description string, properties map[string]any) *responses.FunctionToolParam {
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
