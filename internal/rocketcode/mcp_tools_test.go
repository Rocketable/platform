package rocketcode

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Rocketable/platform/internal/rocketcode/codemode"
	"github.com/Rocketable/platform/internal/rocketcode/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPToolsOmitsWithoutAllow(t *testing.T) {
	t.Parallel()

	reg, err := mcpclient.New(t.TempDir(), map[string]mcpclient.ServerConfig{
		"demo": {URL: "http://127.0.0.1:9"},
	})
	require.NoError(t, err)

	factory := &toolFactory{mcpRegistry: reg, baseTools: makeSandboxedTools(nil, nil)}
	assert.Nil(t, factory.mcpToolsFor(&Agent{}, nil))

	var denied PermissionSet
	require.NoError(t, denied.Deny("mcp", "demo.*"))
	assert.Nil(t, factory.mcpToolsFor(&Agent{Permission: denied}, nil))

	var otherServer PermissionSet
	require.NoError(t, otherServer.Allow("mcp", "missing.*"))
	assert.Nil(t, factory.mcpToolsFor(&Agent{Permission: otherServer}, nil))
}

func TestAssembleToolsHidesHostFromModel(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })

	var permissions PermissionSet
	require.NoError(t, permissions.Allow("read", "*"))
	require.NoError(t, permissions.Allow("bash", "echo *"))

	sfs := &sandboxedFileSystem{mu: sync.Mutex{}, root: root}
	outputDir := filepath.Join(dir, ".tmp", "shell-tmp")
	require.NoError(t, root.MkdirAll(filepath.Join(".tmp", "shell-tmp"), 0o700))
	shellTemp := testShellTempConfig(t, root, outputDir)
	sss := newSandboxedShellSystem(root, &shellTemp, nil, DefaultShellCommand)
	factory := &toolFactory{
		baseTools: makeSandboxedTools(sfs, sss),
	}
	agent := &Agent{Permission: permissions}

	model, hosts := factory.assembleTools(agent)
	require.Contains(t, model, executeToolName)
	require.Contains(t, model, loadExecuteResultToolName)
	require.NotContains(t, hosts, loadExecuteResultToolName)
	loader := model[loadExecuteResultToolName].Definition
	require.True(t, loader.Strict.Value)
	require.Equal(t, false, loader.Parameters["additionalProperties"])
	require.Equal(t, []string{"limit", "line_numbers", "result_id", "start_line"}, loader.Parameters["required"])
	propsLoader := loader.Parameters["properties"].(map[string]any)
	require.Len(t, propsLoader, 4)
	require.Contains(t, loader.Description.Value, "omitted tail cannot be fetched")

	for _, entry := range buildCodeModeSearchIndex(hosts, nil) {
		require.NotEqual(t, loadExecuteResultToolName, entry.item.Path)
	}

	assert.NotContains(t, model, "read")
	assert.NotContains(t, model, "bash")
	assert.Contains(t, hosts, "read")
	assert.Contains(t, hosts, "bash")

	execDesc := model[executeToolName].Definition.Description.Value
	assert.Contains(t, execDesc, "search(")
	assert.Contains(t, execDesc, "gather(")
	assert.Contains(t, execDesc, "gather/map/race/race_first")
	assert.Contains(t, execDesc, "No import/from")
	assert.Contains(t, execDesc, `bash(command=...) takes r'''...''' only`)
	assert.Contains(t, execDesc, `execute code is a JSON string`)
	assert.Contains(t, execDesc, `{"code":`)
	assert.Contains(t, execDesc, `r"..."`)
	assert.Contains(t, execDesc, `r'''`)
	assert.Contains(t, execDesc, "single-line")
	assert.Contains(t, execDesc, "nothing ran")
	assert.Contains(t, execDesc, "str(result)")
	assert.Contains(t, execDesc, "not varargs")
	assert.Contains(t, execDesc, `gather([lambda: read(filePath="a")`)
	assert.NotContains(t, execDesc, `bash(command="`)

	props, _ := model[executeToolName].Definition.Parameters["properties"].(map[string]any)
	codeProp, _ := props["code"].(map[string]any)
	codeDesc, _ := codeProp["description"].(string)
	assert.Contains(t, codeDesc, "Starlark source")
	assert.Contains(t, codeDesc, `bash(command=...) takes r'''...''' only`)
	assert.Contains(t, codeDesc, `execute code is a JSON string`)
	assert.Contains(t, codeDesc, `{"code":`)
	assert.Contains(t, codeDesc, `r"..."`)
	assert.Contains(t, codeDesc, `r'''`)
	assert.Contains(t, codeDesc, "single-line")
	assert.Contains(t, codeDesc, "not varargs")

	prompt := withCodeModeSystemPrompt("base", model, hosts, nil)
	assert.Contains(t, prompt, "## Code Mode")
	assert.Contains(t, prompt, "read(")
	assert.Contains(t, prompt, "No import/from")
	assert.Contains(t, prompt, `bash(command=...) takes r'''...''' only`)
	assert.Contains(t, prompt, `execute code is a JSON string`)
	assert.Contains(t, prompt, `{"code":`)
	assert.Contains(t, prompt, `r"..."`)
	assert.Contains(t, prompt, `r'''`)
	assert.Contains(t, prompt, "single-line")
	assert.Contains(t, prompt, "str(result)")
	assert.Contains(t, prompt, "Concurrency (callables is one list")
	assert.Contains(t, prompt, "gather([lambda:")
	assert.Contains(t, prompt, "race_first([lambda:")
	assert.Contains(t, prompt, `gather([lambda: read(filePath="a")`)
	assert.Contains(t, prompt, "Default concurrency 16")
	assert.Contains(t, hosts["bash"].Definition.Description.Value, `bash(command=...) takes r'''...''' only`)
	assert.Contains(t, hosts["bash"].Definition.Description.Value, `{"code":`)
	assert.NotContains(t, prompt, `bash(command="`)

	entries := concurrencySearchEntries()
	require.NotEmpty(t, entries)
	assert.Contains(t, entries[0].item.Signature, "gather([lambda:")
	assert.Contains(t, entries[0].item.Description, "not varargs")
}

func TestExecuteAvailableWithHostToolsOnly(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })

	var permissions PermissionSet
	require.NoError(t, permissions.Allow("read", "*"))

	sfs := &sandboxedFileSystem{mu: sync.Mutex{}, root: root}
	factory := &toolFactory{
		baseTools: makeSandboxedTools(sfs, nil),
	}
	agent := &Agent{Permission: permissions}
	model, hosts := factory.assembleTools(agent)
	require.Contains(t, model, executeToolName)
	require.Len(t, model, 2) // Execute and loader; no skill/task/websearch without grants.
	// websearch may appear if base has it and hasActionableRule - websearch needs grant
	assert.NotContains(t, model, "read")
	assert.Contains(t, hosts, "read")
	assert.NotContains(t, hosts, "bash")
}

func TestCustomToolsAreCodeModeOnlyInsideExecute(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })

	var permissions PermissionSet
	require.NoError(t, permissions.Allow("rocketclaw", "ask_user_question"))
	require.NoError(t, permissions.Allow("rocketclaw", "rocketclaw_reload"))

	custom, err := customLooperTools([]Tool{
		{Name: "rocketclaw_denied", Permission: "rocketclaw", Call: func(context.Context, json.RawMessage, chan<- ChatResponse) (ToolResult, error) {
			return TextToolResult("denied"), nil
		}},
		{
			Name:               "ask_user_question",
			Description:        "Ask the human a question",
			Permission:         "rocketclaw",
			VisibilitySubjects: []string{"ask_user_question"},
			Parameters: map[string]any{
				"properties": map[string]any{
					"question": map[string]any{"type": "string"},
				},
			},
			Call: func(_ context.Context, raw json.RawMessage, _ chan<- ChatResponse) (ToolResult, error) {
				var params struct {
					Question string `json:"question"`
				}
				if err := json.Unmarshal(raw, &params); err != nil {
					return ToolResult{}, fmt.Errorf("unmarshal ask_user_question args: %w", err)
				}

				return TextToolResult("answer:" + params.Question), nil
			},
		},
		{
			Name:               "rocketclaw_reload",
			Description:        "Reload runtime",
			Permission:         "rocketclaw",
			VisibilitySubjects: []string{"rocketclaw_reload"},
			Parameters: map[string]any{
				"properties": map[string]any{
					"reason": map[string]any{"type": "string"},
				},
			},
			Call: func(context.Context, json.RawMessage, chan<- ChatResponse) (ToolResult, error) {
				return TextToolResult("reloaded"), nil
			},
		},
	}, nil)
	require.NoError(t, err)

	sfs := &sandboxedFileSystem{mu: sync.Mutex{}, root: root}
	base := makeSandboxedTools(sfs, nil)
	maps.Copy(base, custom)
	factory := &toolFactory{baseTools: base}
	agent := &Agent{Permission: permissions}
	model, hosts := factory.assembleTools(agent)

	// Platform tools run only inside execute; the model sees execute and its result loader.
	assert.ElementsMatch(t, []string{executeToolName, loadExecuteResultToolName}, slices.Collect(maps.Keys(model)))
	assert.ElementsMatch(t, []string{"ask_user_question", "rocketclaw_reload"}, slices.Collect(maps.Keys(hosts)))

	output := make(chan ChatResponse, 8)
	observations := &turnObservations{journal: InertJournal{}}
	looper := &looper{Journal: InertJournal{}, observations: observations, Permissions: permissions, Tools: model, CodeModeHosts: hosts, Diagnostics: true}
	ctx := withToolCallContext(t.Context(), looper, output, "exec-1")
	run := model[executeToolName]
	result, err := run.Call(ctx, json.RawMessage(`{"code":"def main():\n    return ask_user_question(question=\"ship it?\")\n"}`), output, emptyToolCallMetadata())
	require.NoError(t, err)
	assert.Equal(t, "answer:ship it?", result.Output)
	close(output)

	var sawNested bool

	for item := range output {
		if item.Tool != nil && item.Tool.Name == executeNestedToolPrefix+"ask_user_question" {
			sawNested = true
		}
	}

	assert.True(t, sawNested, "expected nested ask_user_question diagnostic")

	// The nested call and its result are saved in the turn trace under the execute call.
	require.Len(t, observations.trace, 2)

	var (
		call   hostCallTrace
		answer hostResultTrace
	)

	require.NoError(t, json.Unmarshal(observations.trace[0], &call))
	require.NoError(t, json.Unmarshal(observations.trace[1], &answer))
	assert.Equal(t, hostCallTrace{Type: "function_call", CallID: call.CallID, ParentCallID: "exec-1", Name: "ask_user_question", Arguments: `{"question":"ship it?"}`}, call)
	assert.Equal(t, hostResultTrace{Type: "function_call_output", CallID: call.CallID, ParentCallID: "exec-1", Output: "answer:ship it?"}, answer)
	assert.NotEmpty(t, call.CallID)
}

func TestCodeModeHostToolsIncludesBashWhenAllowed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })

	outputDir := filepath.Join(dir, ".tmp", "shell-tmp")
	require.NoError(t, root.MkdirAll(filepath.Join(".tmp", "shell-tmp"), 0o700))
	shellTemp := testShellTempConfig(t, root, outputDir)

	var permissions PermissionSet
	require.NoError(t, permissions.Allow("bash", "echo *"))
	require.NoError(t, permissions.Deny("bash", "rm *"))

	sfs := &sandboxedFileSystem{mu: sync.Mutex{}, root: root}
	sss := newSandboxedShellSystem(root, &shellTemp, nil, DefaultShellCommand)
	factory := &toolFactory{
		baseTools: makeSandboxedTools(sfs, sss),
	}
	agent := &Agent{Permission: permissions}

	model, hosts := factory.assembleTools(agent)
	assert.NotContains(t, model, "bash")
	assert.Contains(t, hosts, "bash")

	looper := &looper{
		Journal:                InertJournal{},
		observations:           &turnObservations{journal: InertJournal{}},
		Permissions:            permissions,
		AutoApprovePermissions: true,
		Tools:                  model,
		CodeModeHosts:          hosts,
	}
	ctx := withToolCallContext(t.Context(), looper, nil, "")
	bound, _ := codeModeHostToolsFromContext(ctx)

	var bashTool *struct {
		Call func(context.Context, map[string]any) (string, error)
	}

	for i := range bound {
		if bound[i].Name == "bash" {
			bashTool = &struct {
				Call func(context.Context, map[string]any) (string, error)
			}{Call: bound[i].Call}

			break
		}
	}

	require.NotNil(t, bashTool)

	out, err := bashTool.Call(ctx, map[string]any{"command": "echo hi"})
	require.NoError(t, err)
	assert.Contains(t, out, "hi")

	_, err = bashTool.Call(ctx, map[string]any{"command": "rm -rf /tmp/x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "denied")
}

func TestCodeModeHostsSurviveModelWithoutHosts(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	require.NoError(t, root.WriteFile("a.txt", []byte("hello"), 0o600))

	var permissions PermissionSet
	require.NoError(t, permissions.Allow("read", "*"))

	sfs := &sandboxedFileSystem{mu: sync.Mutex{}, root: root}
	factory := &toolFactory{baseTools: makeSandboxedTools(sfs, nil)}
	agent := &Agent{Permission: permissions}
	model, hosts := factory.assembleTools(agent)

	looper := &looper{Journal: InertJournal{}, observations: &turnObservations{journal: InertJournal{}}, Permissions: permissions, Tools: model, CodeModeHosts: hosts}
	ctx := withToolCallContext(t.Context(), looper, nil, "")

	run := model[executeToolName]
	result, err := run.Call(ctx, json.RawMessage(`{"code":"def main():\n    return read(filePath=\"a.txt\")\n"}`), nil, emptyToolCallMetadata())
	require.NoError(t, err)
	assert.Contains(t, result.Output, "hello")

	full := strings.Repeat("line\n", 2100)
	require.NoError(t, root.WriteFile("large.txt", []byte(full), 0o600))
	looper.promptExpansion = promptExpansionEnvironment{root: root}
	looper.spillRel = defaultSpillRel
	looper.restoreTurnExecuteResults("turn")
	t.Cleanup(looper.deleteTurnExecuteResults)

	result, err = run.Call(ctx, json.RawMessage(`{"code":"def main():\n    text = read(filePath=\"large.txt\")\n    if \"2100: line\" not in text:\n        fail(\"host output clipped\")\n    return \"full host content\"\n"}`), nil, emptyToolCallMetadata())
	require.NoError(t, err)
	require.Equal(t, "full host content", result.Output)
	require.Empty(t, looper.spillResults)

	for _, code := range []string{"def main():\n    return read(filePath=\"large.txt\")\n", "def main():\n    return \"line\\n\" * 2100\n"} {
		raw, err := json.Marshal(executeParams{Code: code})
		require.NoError(t, err)
		result, err = run.Call(ctx, raw, nil, emptyToolCallMetadata())
		require.NoError(t, err)
		require.Contains(t, result.Output, "load_execute_result")
	}

	require.Len(t, looper.spillResults, 2)
}

func TestExecuteBashOutputSurvivesContainers(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll(".tmp/shell", 0o700))
	shellTemp := testShellTempConfig(t, root, filepath.Join(dir, ".tmp", "shell"))
	sss := newSandboxedShellSystem(root, &shellTemp, nil, DefaultShellCommand)
	sfs := &sandboxedFileSystem{mu: sync.Mutex{}, root: root}

	var permissions PermissionSet
	require.NoError(t, permissions.Allow("bash", "*"))

	factory := &toolFactory{baseTools: makeSandboxedTools(sfs, sss)}
	model, hosts := factory.assembleTools(&Agent{Permission: permissions})
	looper := &looper{Journal: InertJournal{}, observations: &turnObservations{journal: InertJournal{}}, Permissions: permissions, Tools: model, CodeModeHosts: hosts}
	ctx := withToolCallContext(t.Context(), looper, nil, "")

	for _, test := range []struct{ name, expression, want string }{
		{"direct", `bash(command=r'''printf first''')`, "first"},
		{"gather", `gather([lambda: bash(command=r'''printf first'''), lambda: bash(command=r'''printf second'''), lambda: bash(command=r'''printf third''')])`, `["first","second","third"]`},
		{"nested", `{"results": [gather([lambda: bash(command=r'''printf first''')])]}`, `{"results":[["first"]]}`},
		{"escaped", `[bash(command=r'''printf '%s\n' 'first "quoted" \path 日本語' second''')]`, `["first \"quoted\" \\path 日本語\nsecond\n"]`},
		{"failure", `[bash(command=r'''printf failed >&2; exit 7''')]`, `["failed"]`},
		{"error_code", `bash(command=r'''exit 7''').error_code`, "7"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(struct {
				Code string `json:"code"`
			}{"def main():\n    return " + test.expression + "\n"})
			require.NoError(t, err)
			result, err := model[executeToolName].Call(ctx, raw, nil, emptyToolCallMetadata())
			require.NoError(t, err)
			require.Equal(t, test.want, result.Output)
		})
	}
}

func TestExecuteParseFailureDoesNotRunHost(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })

	var permissions PermissionSet
	require.NoError(t, permissions.Allow("read", "*"))

	sfs := &sandboxedFileSystem{mu: sync.Mutex{}, root: root}
	factory := &toolFactory{baseTools: makeSandboxedTools(sfs, nil)}
	agent := &Agent{Permission: permissions}
	model, hosts := factory.assembleTools(agent)
	looper := &looper{Journal: InertJournal{}, observations: &turnObservations{journal: InertJournal{}}, Permissions: permissions, Tools: model, CodeModeHosts: hosts}
	ctx := withToolCallContext(t.Context(), looper, nil, "")

	run := model[executeToolName]
	_, err = run.Call(ctx, json.RawMessage(`{"code":"def main():\n    return bash(command=r\"python3 - <<'PY'\nprint(\"hello\")\nPY\")\n"}`), nil, emptyToolCallMetadata())
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"phase":"codemode_parse"`)
	assert.Contains(t, err.Error(), `"execution_started":false`)
	assert.Contains(t, err.Error(), "unexpected newline")
}

func TestExecuteNestedToolEmitsThinkingDiagnostic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	require.NoError(t, root.WriteFile("a.txt", []byte("hello"), 0o600))

	var permissions PermissionSet
	require.NoError(t, permissions.Allow("read", "*"))

	sfs := &sandboxedFileSystem{mu: sync.Mutex{}, root: root}
	factory := &toolFactory{baseTools: makeSandboxedTools(sfs, nil)}
	agent := &Agent{Permission: permissions}
	model, hosts := factory.assembleTools(agent)

	output := make(chan ChatResponse, 8)
	looper := &looper{Journal: InertJournal{}, observations: &turnObservations{journal: InertJournal{}}, Permissions: permissions, Tools: model, CodeModeHosts: hosts, Diagnostics: true}
	ctx := withToolCallContext(t.Context(), looper, output, "")

	run := model[executeToolName]
	result, err := run.Call(ctx, json.RawMessage(`{"code":"def main():\n    return read(filePath=\"a.txt\")\n"}`), output, emptyToolCallMetadata())
	require.NoError(t, err)
	assert.Contains(t, result.Output, "hello")
	close(output)

	var sawNested bool

	for item := range output {
		if item.Tool != nil && item.Tool.Phase == toolDiagnosticPhaseCall && item.Tool.Name == executeNestedToolPrefix+"read" {
			sawNested = true

			assert.Contains(t, string(item.Tool.Arguments), "a.txt")
		}
	}

	assert.True(t, sawNested, "expected nested execute → read diagnostic")
}

func TestExecuteNestedConcurrencyPrefixesThinkingDiagnostic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	require.NoError(t, root.WriteFile("a.txt", []byte("a"), 0o600))
	require.NoError(t, root.WriteFile("b.txt", []byte("b"), 0o600))

	var permissions PermissionSet
	require.NoError(t, permissions.Allow("read", "*"))

	sfs := &sandboxedFileSystem{mu: sync.Mutex{}, root: root}
	factory := &toolFactory{baseTools: makeSandboxedTools(sfs, nil)}
	model, hosts := factory.assembleTools(&Agent{Permission: permissions})
	output := make(chan ChatResponse, 8)
	looper := &looper{Journal: InertJournal{}, observations: &turnObservations{journal: InertJournal{}}, Permissions: permissions, Tools: model, CodeModeHosts: hosts, Diagnostics: true}
	ctx := withToolCallContext(t.Context(), looper, output, "")

	run := model[executeToolName]
	result, err := run.Call(ctx, json.RawMessage(`{"code":"def main():\n    return gather([lambda: read(filePath=\"a.txt\"), lambda: read(filePath=\"b.txt\")])\n"}`), output, emptyToolCallMetadata())
	require.NoError(t, err)
	assert.Contains(t, result.Output, "a")
	assert.Contains(t, result.Output, "b")
	close(output)

	var names []string

	for item := range output {
		if item.Tool != nil && item.Tool.Phase == toolDiagnosticPhaseCall {
			names = append(names, item.Tool.Name)
		}
	}

	assert.ElementsMatch(t, []string{
		executeNestedToolPrefix + "gather → read",
		executeNestedToolPrefix + "gather → read",
	}, names)
}

func TestExecuteNestedSearchEmitsThinkingDiagnostic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })

	var permissions PermissionSet
	require.NoError(t, permissions.Allow("read", "*"))

	sfs := &sandboxedFileSystem{mu: sync.Mutex{}, root: root}
	factory := &toolFactory{baseTools: makeSandboxedTools(sfs, nil)}
	agent := &Agent{Permission: permissions}
	model, hosts := factory.assembleTools(agent)

	output := make(chan ChatResponse, 8)
	looper := &looper{Journal: InertJournal{}, observations: &turnObservations{journal: InertJournal{}}, Permissions: permissions, Tools: model, CodeModeHosts: hosts, Diagnostics: true}
	ctx := withToolCallContext(t.Context(), looper, output, "")

	run := model[executeToolName]
	result, err := run.Call(ctx, json.RawMessage(`{"code":"def main():\n    return search(query=\"context7\")\n"}`), output, emptyToolCallMetadata())
	require.NoError(t, err)
	assert.Contains(t, result.Output, "items")
	close(output)

	var sawNested bool

	for item := range output {
		if item.Tool != nil && item.Tool.Phase == toolDiagnosticPhaseCall && item.Tool.Name == executeNestedToolPrefix+"search" {
			sawNested = true

			assert.Contains(t, string(item.Tool.Arguments), "context7")
		}
	}

	assert.True(t, sawNested, "expected nested execute → search diagnostic")
}

func TestExecuteNestedToolDiagnosticNotDroppedWhenOutputFull(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	require.NoError(t, root.WriteFile("a.txt", []byte("hello"), 0o600))

	var permissions PermissionSet
	require.NoError(t, permissions.Allow("read", "*"))

	sfs := &sandboxedFileSystem{mu: sync.Mutex{}, root: root}
	factory := &toolFactory{baseTools: makeSandboxedTools(sfs, nil)}
	agent := &Agent{Permission: permissions}
	model, hosts := factory.assembleTools(agent)

	// Unbuffered channel: non-blocking emit would drop; nested emit must block until read.
	output := make(chan ChatResponse)
	looper := &looper{Journal: InertJournal{}, observations: &turnObservations{journal: InertJournal{}}, Permissions: permissions, Tools: model, CodeModeHosts: hosts, Diagnostics: true}
	ctx := withToolCallContext(t.Context(), looper, output, "")

	done := make(chan struct{})

	var nestedName string

	go func() {
		defer close(done)

		for item := range output {
			if item.Tool != nil && item.Tool.Name == executeNestedToolPrefix+"read" {
				nestedName = item.Tool.Name
				return
			}
		}
	}()

	run := model[executeToolName]
	result, err := run.Call(ctx, json.RawMessage(`{"code":"def main():\n    return read(filePath=\"a.txt\")\n"}`), output, emptyToolCallMetadata())
	require.NoError(t, err)
	assert.Contains(t, result.Output, "hello")
	close(output)
	<-done
	assert.Equal(t, executeNestedToolPrefix+"read", nestedName)
}

func TestMCPServerVisibility(t *testing.T) {
	t.Parallel()

	reg, err := mcpclient.New(t.TempDir(), map[string]mcpclient.ServerConfig{
		"demo": {URL: "http://127.0.0.1:9"},
		"acme": {URL: "http://127.0.0.1:9"},
	})
	require.NoError(t, err)

	for _, tc := range []struct {
		name, rules string
		want        []string
	}{
		{"server wildcard", `mcp: {"demo.*": allow}`, []string{"demo", "demo"}},
		{"auto exact tool", `mcp: {"acme.echo": auto}`, []string{"acme"}},
		{"bare server", `mcp: {"demo": allow}`, []string{"demo", "demo"}},
		{"all servers", `mcp: {"*": allow}`, []string{"demo", "acme", "demo", "hidden"}},
		{"wildcard server", `mcp: {"*.echo": auto}`, []string{"demo", "acme", "demo", "hidden"}},
		{"blank pattern", `mcp: {"   ": allow}`, []string{"demo", "acme", "demo", "hidden"}},
		{"mixed rules", "read: {'*': allow}\nmcp: {'demo.other': deny, 'acme.echo': auto, 'demo.echo': allow, 'demo.*': allow, '*': deny}", []string{"demo", "acme", "demo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			permissions := parsePermissionYAML(t, tc.rules)
			require.Equal(t, tc.want, visibleMCPServers(permissions, []string{"demo", "acme", "demo", "hidden"}))

			tools := (&toolFactory{mcpRegistry: reg}).mcpToolsFor(&Agent{Permission: permissions}, nil)
			require.Len(t, tools, 2)
			assert.Equal(t, executeToolName, tools[executeToolName].Definition.Name)
			assert.Equal(t, []string{"code_mode_approve"}, tools[executeToolName].VisibilitySubjects)
		})
	}

	prompt := codeModeSystemPrompt(nil, []string{"demo"})
	assert.Contains(t, prompt, "demo")
	assert.NotContains(t, prompt, "acme")
}

func TestExecuteExactToolGrant(t *testing.T) {
	t.Parallel()

	reg, err := mcpclient.New(t.TempDir(), map[string]mcpclient.ServerConfig{
		"github": {URL: "http://127.0.0.1:9"},
	})
	require.NoError(t, err)

	var permissions PermissionSet
	require.NoError(t, permissions.Allow("mcp", "github.create_issue"))
	require.NoError(t, permissions.Set("rocketclaw", codeModeApproveSubject, PermissionAuto))

	tools := (&toolFactory{mcpRegistry: reg}).mcpToolsFor(&Agent{Permission: permissions}, nil)
	require.NotNil(t, tools)
	assert.Equal(t, []string{"code_mode_approve"}, tools[executeToolName].VisibilitySubjects)

	loop := testLooper(mockResponses())
	loop.Permissions = permissions
	loop.AutoApprovePermissions = true
	execute := tools[executeToolName]
	decision, err := loop.permissionDecision(executeToolName, &execute, json.RawMessage(`{"code":"def main(): return github_create_issue()"}`))
	require.NoError(t, err)
	require.NotNil(t, decision.review)
	require.Equal(t, "rocketclaw", decision.review.Permission)
}

func TestLoadExecuteResultDispatchWithoutRead(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	full := strings.Repeat("line\n", 2100)
	url := startRocketCodeMCPHTTPServer(t, func(server *mcp.Server) {
		mcp.AddTool(server, &mcp.Tool{Name: "output"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: full}}}, nil, nil
		})
	})
	reg, err := mcpclient.New(root.Name(), map[string]mcpclient.ServerConfig{"demo": {URL: url}})
	require.NoError(t, err)
	permissions := parsePermissionYAML(t, "mcp: {'demo.*': allow}\nrocketclaw: {'*': auto}")
	factory := &toolFactory{mcpRegistry: reg, baseTools: makeSandboxedTools(&sandboxedFileSystem{root: root}, nil), spillRel: defaultSpillRel, promptExpansion: promptExpansionEnvironment{root: root}}
	loop := testLooper(mockResponses())
	loop.agent = Agent{Name: "main", Permission: permissions}
	loop.Permissions = permissions
	loop.Tools, loop.CodeModeHosts = factory.assembleTools(&loop.agent)
	factory.configureSpill(loop)
	loop.restoreTurnExecuteResults("turn-1")
	t.Cleanup(loop.deleteTurnExecuteResults)

	reviewer := permissionReviewerWith(permissionReviewDecision{Outcome: permissionReviewOutcomeDeny})
	loop.PermissionReviewer = reviewer
	loop.AutoApprovePermissions = true

	results, _, err := loop.dispatchToolCalls(t.Context(), responseWithFunctionCalls("resp", []responses.ResponseFunctionToolCall{testFunctionCall("tool", "call", executeToolName, `{"code":"def main():\n    text = demo_output()\n    if len(text.splitlines()) != 2100:\n        fail(\"host output clipped\")\n    return text\n"}`)}), nil, nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Contains(t, results[0].Result.Output, "load_execute_result")
	_, footer, ok := strings.Cut(results[0].Result.Output, "result_id=\"")
	require.True(t, ok)
	id, _, ok := strings.Cut(footer, "\"")
	require.True(t, ok)

	pageArgs := fmt.Sprintf(`{"result_id":%q,"start_line":2001,"limit":10,"line_numbers":true}`, id)
	loop.Diagnostics = true

	for range 2 {
		output := make(chan ChatResponse, 10)
		results, _, err = loop.dispatchToolCalls(t.Context(), responseWithFunctionCalls("page", []responses.ResponseFunctionToolCall{testFunctionCall("page", "page-call", "load_execute_result", pageArgs)}), nil, output)
		require.NoError(t, err)

		var want strings.Builder
		for i := 2001; i <= 2010; i++ {
			fmt.Fprintf(&want, "%d: line\n", i)
		}

		want.WriteString("\n[next_start_line=2011]\n")
		require.Equal(t, want.String(), results[0].Result.Output)
		close(output)

		for _, response := range collectResponses(output) {
			if response.Tool != nil {
				require.Equal(t, loadExecuteResultToolName, response.Tool.Name)
			}
		}
	}

	require.Equal(t, permissions, loop.Permissions)
	require.Empty(t, loop.CodeModeHosts)
	require.Empty(t, reviewedRequests(reviewer))
	require.Len(t, loop.spillResults, 1)

	read := factory.baseTools["read"]
	decision, err := loop.permissionDecision("read", &read, json.RawMessage(`{"filePath":"secret.txt"}`))
	require.NoError(t, err)
	require.True(t, decision.denied)

	for args, want := range map[string]string{`{"result_id":"missing"}`: "unknown or expired execute result", `{"result_id":"../../etc/passwd"}`: "unknown or expired execute result", `{"result_id":1}`: "check permission"} {
		results, _, err := loop.dispatchToolCalls(t.Context(), responseWithFunctionCalls("invalid", []responses.ResponseFunctionToolCall{testFunctionCall("invalid", "invalid-call", loadExecuteResultToolName, args)}), nil, nil)
		require.NoError(t, err)
		require.Contains(t, results[0].Result.Output, want)
	}

	require.Empty(t, reviewedRequests(reviewer))

	loader := loop.Tools[loadExecuteResultToolName]
	sibling := testLooper(mockResponses())
	factory.configureSpill(sibling)
	sibling.restoreTurnExecuteResults("sibling")
	t.Cleanup(sibling.deleteTurnExecuteResults)
	ctx := withToolCallContext(t.Context(), sibling, nil, "")
	_, err = loader.Call(ctx, json.RawMessage(pageArgs), nil, emptyToolCallMetadata())
	require.EqualError(t, err, "unknown or expired execute result")
	_, err = sibling.saveExecuteResult(strings.Repeat("own\n", 2100))
	require.NoError(t, err)

	for siblingID := range sibling.spillResults {
		raw, err := json.Marshal(loadExecuteResultParams{ResultID: siblingID, Limit: 1})
		require.NoError(t, err)
		page, err := loader.Call(ctx, raw, nil, emptyToolCallMetadata())
		require.NoError(t, err)
		require.Equal(t, "own\n\n[next_start_line=2]\n", page.Output)
	}

	files, err := root.Open(defaultSpillRel + "/turn-1")
	require.NoError(t, err)

	defer func() { require.NoError(t, files.Close()) }()

	names, err := files.Readdirnames(-1)
	require.NoError(t, err)
	require.Len(t, names, 1)
}

func TestExecuteWholeScriptApproval(t *testing.T) {
	const code = `{"code":"def main():\n    return read(filePath=\"allowed\")\n"}`

	for _, tc := range []struct {
		name, rules, reviewer string
		outcome               permissionReviewOutcome
		wantCalls             int
		wantReview            bool
	}{
		{"unset with wildcard auto", "rocketclaw: {'*': auto}\nread: {allowed: allow}", "", permissionReviewOutcomeAllow, 1, false},
		{"unset with wildcard deny", "rocketclaw: {'*': deny}\nread: {allowed: allow}", "", permissionReviewOutcomeAllow, 1, false},
		{"explicit allow overrides wildcard auto", "rocketclaw: {code_mode_approve: allow, '*': auto}\nread: {allowed: allow}", "", permissionReviewOutcomeAllow, 1, false},
		{"built-in reviewer", "rocketclaw: {code_mode_approve: auto}\nread: {allowed: allow}", "", permissionReviewOutcomeAllow, 1, true},
		{"custom reviewer overrides wildcard allow", "rocketclaw: {code_mode_approve: 'auto(release-reviewer)', '*': allow}\nread: {allowed: allow}", "release-reviewer", permissionReviewOutcomeAllow, 1, true},
		{"last exact rule wins", "rocketclaw: {code_mode_approve: allow, code_mode_approve: 'auto(release-reviewer)', '*': deny}\nread: {allowed: allow}", "release-reviewer", permissionReviewOutcomeAllow, 1, true},
		{"review denial prevents script", "rocketclaw: {code_mode_approve: 'auto(release-reviewer)'}\nread: {allowed: allow}", "release-reviewer", permissionReviewOutcomeDeny, 0, true},
		{"entry approval does not grant nested read", "rocketclaw: {code_mode_approve: auto}\nread: {another: allow}", "", permissionReviewOutcomeAllow, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			permissions := parsePermissionYAML(t, tc.rules)
			calls := 0
			readTool := testLooperTool("read")
			readTool.Permission = "read"
			readTool.Subjects = func(raw json.RawMessage) ([]string, error) {
				var args struct {
					FilePath string `json:"filePath"`
				}
				if err := json.Unmarshal(raw, &args); err != nil {
					return nil, fmt.Errorf("decode read subject: %w", err)
				}

				return []string{args.FilePath}, nil
			}
			readTool.Call = func(context.Context, json.RawMessage, chan<- ChatResponse, toolCallMetadata) (ToolResult, error) {
				calls++
				return TextToolResult("read result"), nil
			}
			hosts := map[string]looperTool{"read": readTool}
			model := (&toolFactory{}).mcpToolsFor(&Agent{Permission: permissions}, hosts)
			loop := testLooper(mockResponses())
			loop.agent = Agent{Name: "main"}
			loop.Permissions = permissions
			loop.Tools = model
			loop.CodeModeHosts = hosts
			loop.AutoApprovePermissions = true
			reviewer := permissionReviewerWith(permissionReviewDecision{Outcome: tc.outcome, Rationale: "review decision"})
			loop.PermissionReviewer = reviewer
			output := make(chan ChatResponse, 10)

			results, _, err := loop.dispatchToolCalls(t.Context(), responseWithFunctionCalls("resp", []responses.ResponseFunctionToolCall{testFunctionCall("tool", "call", executeToolName, code)}), nil, output)
			require.NoError(t, err)
			require.Len(t, results, 1)
			require.Equal(t, tc.wantCalls, calls)

			switch {
			case tc.outcome == permissionReviewOutcomeDeny:
				require.Contains(t, results[0].Result.Output, "automatic permission review rejected")
			case tc.wantCalls == 0:
				require.Contains(t, results[0].Result.Output, `permission "read"`)
			default:
				require.Contains(t, results[0].Result.Output, "read result")
			}

			requests := reviewedRequests(reviewer)
			if !tc.wantReview {
				require.Empty(t, requests)
				return
			}

			require.Len(t, requests, 1)
			require.Equal(t, executeToolName, requests[0].ToolName)
			require.Equal(t, "rocketclaw", requests[0].Permission)
			require.Equal(t, []string{"code_mode_approve"}, requests[0].Subjects)
			require.JSONEq(t, code, requests[0].RawArguments)
			require.Equal(t, tc.reviewer, requests[0].Reviewer)
			require.Equal(t, tc.reviewer == "", requests[0].ReviewerEmbedded)
		})
	}
}

func TestExecuteSearchAndRun(t *testing.T) {
	t.Parallel()

	url := startRocketCodeMCPHTTPServer(t, func(server *mcp.Server) {
		mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "echo text"}, func(_ context.Context, _ *mcp.CallToolRequest, in struct {
			Message string `json:"message"`
		}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Message}}}, nil, nil
		})
		mcp.AddTool(server, &mcp.Tool{Name: "search_issue", Description: "find issues"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "issue"}}}, nil, nil
		})
		mcp.AddTool(server, &mcp.Tool{Name: "danger", Description: "danger"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "no"}}}, nil, nil
		})
	})

	reg, err := mcpclient.New(t.TempDir(), map[string]mcpclient.ServerConfig{"demo": {URL: url}})
	require.NoError(t, err)

	var permissions PermissionSet
	require.NoError(t, permissions.Allow("mcp", "demo.*"))
	require.NoError(t, permissions.Deny("mcp", "demo.danger"))

	tools := (&toolFactory{mcpRegistry: reg}).mcpToolsFor(&Agent{Permission: permissions}, nil)
	require.NotNil(t, tools)

	looper := &looper{
		Journal:                InertJournal{},
		observations:           &turnObservations{journal: InertJournal{}},
		Permissions:            permissions,
		AutoApprovePermissions: false,
	}
	ctx := withToolCallContext(t.Context(), looper, nil, "")

	run := tools[executeToolName]
	result, err := run.Call(ctx, json.RawMessage(`{"code":"def main():\n    return search(query=\"\")\n"}`), nil, emptyToolCallMetadata())
	require.NoError(t, err)
	assert.Contains(t, result.Output, `"path":"demo.echo"`)
	assert.Contains(t, result.Output, `"path":"demo.search_issue"`)
	assert.Contains(t, result.Output, `"callable":"demo_echo"`)
	assert.NotContains(t, result.Output, "danger")

	result, err = run.Call(ctx, json.RawMessage(`{"code":"def main():\n    return search(query=\"issue\", namespace=\"demo\")\n"}`), nil, emptyToolCallMetadata())
	require.NoError(t, err)
	assert.Contains(t, result.Output, "search_issue")
	assert.NotContains(t, result.Output, `"path":"demo.echo"`)

	result, err = run.Call(ctx, json.RawMessage(`{"code":"def main():\n    return demo_echo(message=\"hi\")\n"}`), nil, emptyToolCallMetadata())
	require.NoError(t, err)
	assert.Equal(t, "hi", result.Output)

	_, err = run.Call(ctx, json.RawMessage(`{"code":"def main():\n    return demo_danger()\n"}`), nil, emptyToolCallMetadata())
	require.Error(t, err)
}

func TestCodeModeSearchOpenCodeShape(t *testing.T) {
	t.Parallel()

	hosts := map[string]looperTool{
		"read": {Definition: *functionTool("read", "Read a file", map[string]any{
			"filePath": map[string]any{"type": "string"},
		})},
		"bash": {Definition: *functionTool("bash", "Run shell", map[string]any{
			"command": map[string]any{"type": "string"},
		})},
	}
	mcpTools := []codemode.ToolDesc{{
		Server: "demo", Name: "echo", Description: "echo text",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"message": map[string]any{"type": "string"}}},
	}}

	index := buildCodeModeSearchIndex(hosts, mcpTools)
	out := codeModeSearch(index, map[string]any{"query": "echo", "limit": 10})

	var parsed codeModeSearchResult
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	require.NotEmpty(t, parsed.Items)
	assert.Equal(t, "demo.echo", parsed.Items[0].Path)
	assert.Equal(t, "demo_echo", parsed.Items[0].Callable)
	assert.Contains(t, parsed.Items[0].Signature, "demo_echo(")
	assert.Equal(t, 0, parsed.Remaining)
	assert.Nil(t, parsed.Next)

	out = codeModeSearch(index, map[string]any{"query": "", "limit": 1, "offset": 0})
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	require.Len(t, parsed.Items, 1)
	assert.Equal(t, 6, parsed.Remaining) // 4 concurrency + bash, read, demo.echo → 7 total
	require.NotNil(t, parsed.Next)
	assert.Equal(t, 1, parsed.Next.Offset)

	out = codeModeSearch(index, map[string]any{"query": "gather", "limit": 5})
	require.NoError(t, json.Unmarshal([]byte(out), &parsed))
	require.NotEmpty(t, parsed.Items)
	assert.Equal(t, "gather", parsed.Items[0].Path)
	assert.Equal(t, "gather", parsed.Items[0].Callable)
	assert.Contains(t, parsed.Items[0].Signature, "gather(")
}

func TestToolsForIncludesExecuteWhenConfigured(t *testing.T) {
	t.Parallel()

	reg, err := mcpclient.New(t.TempDir(), map[string]mcpclient.ServerConfig{
		"demo": {URL: "http://127.0.0.1:9"},
	})
	require.NoError(t, err)

	var permissions PermissionSet
	require.NoError(t, permissions.Allow("mcp", "demo.*"))

	factory := &toolFactory{
		mcpRegistry: reg,
		baseTools:   map[string]looperTool{},
		skills:      Skills{Items: map[string]Skill{}},
		agents:      Agents{Items: map[string]Agent{}},
	}

	tools := factory.toolsFor(&Agent{Permission: permissions})
	require.Contains(t, tools, executeToolName)
	require.Contains(t, tools, loadExecuteResultToolName)
	assert.NotContains(t, tools, "read")

	tools = factory.toolsFor(&Agent{})
	require.NotContains(t, tools, executeToolName)
	require.NotContains(t, tools, loadExecuteResultToolName)
}

func startRocketCodeMCPHTTPServer(t *testing.T, register func(*mcp.Server)) string {
	t.Helper()

	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "rocketcode-mcp", Version: "1.0.0"}, nil)
	register(mcpServer)

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{Stateless: true})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()

	t.Cleanup(func() { _ = srv.Close() })

	return "http://" + ln.Addr().String()
}

func TestExecuteReplaysRecordedHostCalls(t *testing.T) {
	var (
		mu       sync.Mutex
		calls    []string
		keys     []string
		base     = t.Context()
		shutdown context.CancelCauseFunc
	)

	host := func(name string) looperTool {
		tool := testLooperTool(name)
		tool.Call = func(ctx context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
			mu.Lock()
			defer mu.Unlock()

			calls = append(calls, name+string(raw))
			keys = append(keys, ToolCallKey(ctx))

			return TextToolResult(name + string(raw)), nil
		}

		return tool
	}

	drain := testLooperTool("drain")
	drain.Call = func(ctx context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
		shutdown(ErrShutdown)

		mu.Lock()
		defer mu.Unlock()

		calls = append(calls, "drain"+string(raw))

		return TextToolResult("drained"), ctx.Err()
	}

	var permissions PermissionSet
	for _, name := range []string{"read", "note", "other", "drain"} {
		require.NoError(t, permissions.Allow(name, "*"))
	}

	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	sfs := &sandboxedFileSystem{mu: sync.Mutex{}, root: root}
	journal := recordingJournal()
	execute := func(code string) (string, error) {
		t.Helper()

		model, _ := (&toolFactory{baseTools: makeSandboxedTools(sfs, nil)}).assembleTools(&Agent{Permission: permissions})
		looper := &looper{Journal: journal, observations: &turnObservations{journal: journal, turnID: "turn-1"}, Permissions: permissions, Tools: model, CodeModeHosts: map[string]looperTool{"note": host("note"), "other": host("other"), "drain": drain}}
		ctx := withToolCallContext(base, looper, nil, "call-1")

		raw, err := json.Marshal(map[string]string{"code": code})
		require.NoError(t, err)

		result, err := model[executeToolName].Call(ctx, raw, nil, emptyToolCallMetadata())

		return result.Output, err
	}
	run := func(code string) string {
		t.Helper()

		output, err := execute(code)
		require.NoError(t, err)

		return output
	}

	script := "def main():\n    a = note(text=\"one\")\n    b = gather([lambda: note(text=\"left\"), lambda: note(text=\"right\")])\n    return a + \"|\" + \"|\".join(b)\n"
	first := run(script)

	require.Len(t, calls, 3)
	require.Len(t, slices.Compact(slices.Sorted(slices.Values(keys))), 3, "each host call, including gather branches, has its own key")

	for _, key := range keys {
		require.True(t, strings.HasPrefix(key, "turn-1/call/call-1/host/"), key)
	}

	calls = nil

	require.Equal(t, first, run(script), "a replayed script returns its recorded results")
	require.Empty(t, calls, "completed host calls and gather branches are not repeated")

	rewrite := func(edit func(*hostCallStep)) {
		t.Helper()

		var step hostCallStep

		found, err := loadStep(t.Context(), journal, keys[0], &step)
		require.NoError(t, err)
		require.True(t, found)
		edit(&step)
		require.NoError(t, saveStep(t.Context(), journal, keys[0], &step))
	}

	calls = nil

	rewrite(func(step *hostCallStep) { step.Done, step.Output = false, "" })

	interrupted, err := execute(script)

	require.Empty(t, calls, "a started, unfinished non-resumable call is not run again")
	require.Contains(t, fmt.Sprint(interrupted, err), "tool call aborted because the runtime stopped")

	rewrite(func(step *hostCallStep) { step.Err = "note failed" })

	failed, err := execute(script)

	require.Empty(t, calls, "a recorded failure is replayed without running the call")
	require.Contains(t, fmt.Sprint(failed, err), "note failed")

	diverged := run("def main():\n    return other(text=\"one\")\n")

	require.Equal(t, []string{`other{"text":"one"}`}, calls, "a recording for a different tool runs live")
	require.Contains(t, diverged, "other")

	calls = nil

	base, shutdown = context.WithCancelCause(t.Context())
	defer shutdown(nil)

	drainScript := "def main():\n    return drain(text=\"x\")\n"
	_, _ = execute(drainScript)

	require.Equal(t, []string{`drain{"text":"x"}`}, calls)

	calls, base = nil, t.Context()

	require.Contains(t, run(drainScript), "drained", "a host call running at shutdown finishes and is recorded")
	require.Empty(t, calls, "the recorded host call is not repeated")
}
