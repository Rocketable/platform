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
	"sync"
	"testing"

	"github.com/Rocketable/platform/internal/rocketcode/codemode"
	"github.com/Rocketable/platform/internal/rocketcode/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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
	require.Len(t, model, 1) // execute only among tools when only read grant (no skill/task/websearch without grants)
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

	require.Contains(t, model, executeToolName)
	// Platform tools stay model-facing and are also nested inside execute.
	assert.Contains(t, model, "ask_user_question")
	assert.Contains(t, model, "rocketclaw_reload")
	assert.Contains(t, hosts, "ask_user_question")
	assert.Contains(t, hosts, "rocketclaw_reload")

	output := make(chan ChatResponse, 8)
	looper := &looper{Permissions: permissions, Tools: model, CodeModeHosts: hosts, Diagnostics: true}
	ctx := withToolCallContext(t.Context(), looper, output)
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
		Permissions:            permissions,
		AutoApprovePermissions: true,
		Tools:                  model,
		CodeModeHosts:          hosts,
	}
	ctx := withToolCallContext(t.Context(), looper, nil)
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

	looper := &looper{Permissions: permissions, Tools: model, CodeModeHosts: hosts}
	ctx := withToolCallContext(t.Context(), looper, nil)

	run := model[executeToolName]
	result, err := run.Call(ctx, json.RawMessage(`{"code":"def main():\n    return read(filePath=\"a.txt\")\n"}`), nil, emptyToolCallMetadata())
	require.NoError(t, err)
	assert.Contains(t, result.Output, "hello")
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
	looper := &looper{Permissions: permissions, Tools: model, CodeModeHosts: hosts}
	ctx := withToolCallContext(t.Context(), looper, nil)

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
	looper := &looper{Permissions: permissions, Tools: model, CodeModeHosts: hosts, Diagnostics: true}
	ctx := withToolCallContext(t.Context(), looper, output)

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
	looper := &looper{Permissions: permissions, Tools: model, CodeModeHosts: hosts, Diagnostics: true}
	ctx := withToolCallContext(t.Context(), looper, output)

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
	looper := &looper{Permissions: permissions, Tools: model, CodeModeHosts: hosts, Diagnostics: true}
	ctx := withToolCallContext(t.Context(), looper, output)

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
	looper := &looper{Permissions: permissions, Tools: model, CodeModeHosts: hosts, Diagnostics: true}
	ctx := withToolCallContext(t.Context(), looper, output)

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

func TestExecuteAllowServerWildcard(t *testing.T) {
	t.Parallel()

	reg, err := mcpclient.New(t.TempDir(), map[string]mcpclient.ServerConfig{
		"demo": {URL: "http://127.0.0.1:9"},
		"acme": {URL: "http://127.0.0.1:9"},
	})
	require.NoError(t, err)

	var permissions PermissionSet
	require.NoError(t, permissions.Allow("mcp", "demo.*"))

	tools := (&toolFactory{mcpRegistry: reg}).mcpToolsFor(&Agent{Permission: permissions}, nil)
	require.Len(t, tools, 1)
	assert.Equal(t, executeToolName, tools[executeToolName].Definition.Name)
	assert.Equal(t, []string{"demo.*"}, tools[executeToolName].VisibilitySubjects)

	prompt := codeModeSystemPrompt(nil, []string{"demo"})
	assert.Contains(t, prompt, "demo")
	assert.NotContains(t, codeModeSystemPrompt(nil, []string{"demo"}), "acme")
}

func TestExecuteExactToolGrant(t *testing.T) {
	t.Parallel()

	reg, err := mcpclient.New(t.TempDir(), map[string]mcpclient.ServerConfig{
		"github": {URL: "http://127.0.0.1:9"},
	})
	require.NoError(t, err)

	var permissions PermissionSet
	require.NoError(t, permissions.Allow("mcp", "github.create_issue"))

	tools := (&toolFactory{mcpRegistry: reg}).mcpToolsFor(&Agent{Permission: permissions}, nil)
	require.NotNil(t, tools)
	assert.Equal(t, []string{"github.create_issue"}, tools[executeToolName].VisibilitySubjects)
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
		Permissions:            permissions,
		AutoApprovePermissions: false,
	}
	ctx := withToolCallContext(t.Context(), looper, nil)

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
	assert.NotContains(t, tools, "read")

	tools = factory.toolsFor(&Agent{})
	require.NotContains(t, tools, executeToolName)
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
