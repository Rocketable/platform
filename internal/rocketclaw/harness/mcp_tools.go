package harness

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
	"github.com/Rocketable/platform/internal/rocketclaw/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openai/openai-go/v3/responses"
	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"golang.org/x/sync/errgroup"
)

const (
	executeToolName           = "execute"
	loadExecuteResultToolName = "load_execute_result"
	searchBuiltinName         = "search"
	mcpPermissionBucket       = "mcp"
	codeModeApproveSubject    = "code_mode_approve"
	AllowBackgroundSubject    = "allow_background"
)

func compileMCPGrant(permission *PermissionSet, facets map[string]map[string][]string, grant map[string]map[string]PermissionAction) error {
	for _, level := range []PermissionAction{PermissionAllow, PermissionAuto} {
		for _, server := range slices.Sorted(maps.Keys(grant)) {
			configured, ok := facets[server]
			if !ok {
				return fmt.Errorf("server %q is not configured", server)
			}

			for _, facet := range slices.Sorted(maps.Keys(grant[server])) {
				patterns, ok := configured[facet]
				if !ok {
					return fmt.Errorf("server %q has no %q facet", server, facet)
				}

				if grant[server][facet] != level {
					continue
				}

				var includes, excepts []string

				for _, pattern := range patterns {
					if tool, removal := strings.CutPrefix(pattern, "-"); removal {
						excepts = append(excepts, server+"."+tool)
					} else {
						includes = append(includes, server+"."+pattern)
					}
				}

				for _, include := range includes {
					_ = permission.Set(mcpPermissionBucket, include, level, excepts...)
				}
			}
		}
	}

	return nil
}

func visibleMCPServers(permissions PermissionSet, servers []string) []string {
	var visible []string

	for _, server := range servers {
		if slices.ContainsFunc(permissions.Buckets, func(bucket PermissionBucket) bool {
			return bucket.Name == mcpPermissionBucket && slices.ContainsFunc(bucket.Rules, func(rule PermissionRule) bool {
				return (rule.Action == PermissionAllow || rule.Action == PermissionAuto) && strings.HasPrefix(rule.Pattern, server+".")
			})
		}) {
			visible = append(visible, server)
		}
	}

	return visible
}

func executeDescription() string {
	return strings.Join([]string{
		"Run a Starlark script that calls tools. Define def main() and return what you want to see; nothing else reaches you (print output is discarded; returning None is an error). Text comes back as text, a file as the image or PDF itself, a list/dict as JSON with files attached as {\"attachment\": n}. Return a failure itself to report one.",
		"Starlark is a Python subset: no import, try/except, classes, f-strings or recursion. json.encode/json.decode are built in. Call tools with keyword arguments only; MCP tools as server_tool(...). Write shell commands as r'''...'''.",
		"",
		"Every call, json.decode included, returns a Result and never raises:",
		"- A success acts like its value, plus r.ok == True.",
		"- A failure has r.ok == False and r.error (\"<tool>: <message>\"). It is falsy and equals no plain value; its methods and attributes return the same failure, and a call given it as (or inside) an argument returns it without running. So a chain of calls ends in its first failure: check .ok once, at the end.",
		"- Test `if not r.ok`, not `if not r`: an empty success (bash that printed nothing) is falsy too.",
		"- Details have the same names on success and failure: .output (bash output, page body), .error_code (bash), .status, .headers (webfetch). A failed bash's methods return the failure; read its text as r.output.",
		"- r.unwrap() returns the value or stops the script with r.error; r.or_else(x) returns the value or x.",
		"- Use str(r) for join(), dict keys and string-method arguments. str(r), like returning r, gives a failure's .error, then its output.",
		"",
		"read(filePath) -> the raw file text, or a file for PNG/JPEG/GIF/WebP/PDF; a sideways JPEG/WebP comes back upright.",
		"bash(command, stdin=text or file) -> its output; a failure on non-zero exit. Save a file, e.g. that upright copy: bash(command=r'''cat > out.jpg''', stdin=f).",
		"glob -> list of paths (.path); grep -> list of matches (.path .line .text).",
		"webfetch(url) -> page text with .status .headers; a failure on HTTP error or non-text.",
		"MCP tools -> text with .structured .content.",
		"A file goes only to bash(stdin=), decision(images=) or the return; elsewhere pass its path or f.base64().",
		"",
		fmt.Sprintf("gather, try_gather, race and race_first take one list of zero-arg lambdas; map and try_map take (items, fn); all take concurrency= (default %d, max %d). gather/map return every Result in order, a crashing branch as a failure; try_gather/try_map return the first failure (cancelling the rest) or the list; race returns the first success, race_first the first to finish. Fan out over a list with map(items, fn) or try_map, not lambdas made in a loop (they all see the last item).", codemode.DefaultConcurrency, codemode.MaxConcurrency),
		"search(query=..., namespace=...) inside a script finds tools and their signatures.",
		"Returns over 2000 lines or 50 KiB of text, or 20 MiB in total, are held back with a result_id: fetch them with the top-level load_execute_result tool, not from a script.",
	}, "\n")
}

func codeModeSystemPrompt(hosts map[string]looperTool, servers []string) string {
	lines := []string{"## Code Mode", ""}

	if len(hosts) > 0 {
		lines = append(lines, "These tools cannot be called directly. They only work inside code you pass to execute:")

		for _, name := range slices.Sorted(maps.Keys(hosts)) {
			tool := hosts[name]

			desc, _, _ := strings.Cut(tool.Definition.Description.Value, "\n")
			if desc = strings.TrimSpace(desc); len(desc) > 120 {
				desc = desc[:117] + "..."
			}

			line := "- " + hostToolSignature(name, codeModeHostInputSchema(name, &tool.Definition))
			if desc != "" {
				line += " // " + desc
			}

			lines = append(lines, line)
		}

		lines = append(lines, "")
	}

	if len(servers) > 0 {
		lines = append(lines, "MCP servers: "+strings.Join(servers, ", ")+". List their tools with search(namespace=\"<server>\").")
	}

	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func withCodeModeSystemPrompt(base string, modelTools, hosts map[string]looperTool, servers []string) string {
	if _, ok := modelTools[executeToolName]; !ok {
		return base
	}

	return strings.TrimSpace(base + "\n\n" + codeModeSystemPrompt(hosts, servers))
}

func (f *toolFactory) mcpToolsFor(agent *Agent, codeHosts map[string]looperTool) map[string]looperTool {
	servers := visibleMCPServers(agent.Permission, f.mcpRegistry.Names())

	if len(servers) == 0 && len(codeHosts) == 0 {
		return nil
	}

	return map[string]looperTool{
		loadExecuteResultToolName: {
			Definition: *functionTool(loadExecuteResultToolName, "Load a page of an oversized execute result, or with attachment=n a file it held back. Top-level only, not callable from scripts. Pages hold at most 2000 lines and 50 KiB; follow next_start_line until EOF.", map[string]any{
				"result_id":    map[string]any{"type": "string", "description": "Opaque result ID from Execute's overflow footer."},
				"start_line":   map[string]any{"type": "integer", "description": "1-based starting source line; 0 means 1. Negative values are invalid."},
				"limit":        map[string]any{"type": "integer", "description": "Maximum source lines; 0 means 2000, positive values capped at 2000. Negative values are invalid."},
				"line_numbers": map[string]any{"type": "boolean", "description": "Prefix each returned line with its original 1-based line number."},
				"attachment":   map[string]any{"type": "integer", "description": "Number n of a file listed as not sent in the Execute result; returns that file as a real attachment. 0 means text pages."},
			}),
			Call: func(ctx context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
				var params loadExecuteResultParams
				if err := decodeToolParams(raw, &params); err != nil {
					return ToolResult{}, err
				}

				tc, _ := toolCallContextFrom(ctx)

				return tc.looper.loadExecuteResult(ctx, f.retainedRel, params)
			},
		},
		executeToolName: f.backgroundTool(agent, BackgroundKindExecute, &looperTool{
			Definition: *functionTool(executeToolName, executeDescription(), map[string]any{
				"code": map[string]any{
					"type":        "string",
					"description": "Starlark source defining def main().",
				},
				"description": map[string]any{"type": "string", "description": executeDescriptionParam},
			}),
			Permission:         "rocketclaw",
			resumable:          true,
			VisibilitySubjects: []string{codeModeApproveSubject},
			Subjects:           func(json.RawMessage) ([]string, error) { return []string{codeModeApproveSubject}, nil },
			Call: func(ctx context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
				return callExecute(ctx, f.mcpRegistry, agent.Permission, servers, f.retainedRel, raw)
			},
		}),
	}
}

type executeParams struct {
	Code string `json:"code"`
}

func listAllowedTools(ctx context.Context, session *mcpclient.Session, permissions PermissionSet, servers []string) ([]codemode.ToolDesc, []string, error) {
	type serverResult struct {
		tools []codemode.ToolDesc
		note  string
	}

	results := make([]serverResult, len(servers))

	var group errgroup.Group

	for i, server := range servers {
		group.Go(func() error {
			infos, err := session.ListTools(ctx, server)
			if err != nil {
				results[i].note = fmt.Sprintf("server %s: %v", server, err)

				return nil
			}

			for _, info := range infos {
				action, _ := permissions.Evaluate(mcpPermissionBucket, server+"."+info.Name)
				if action != PermissionAllow && action != PermissionAuto {
					continue
				}

				results[i].tools = append(results[i].tools, codemode.ToolDesc{
					Server:      server,
					Name:        info.Name,
					Description: info.Description,
					InputSchema: info.InputSchema,
				})
			}

			return nil
		})
	}

	_ = group.Wait()

	var (
		tools []codemode.ToolDesc
		notes []string
	)

	for _, result := range results {
		if result.note != "" {
			notes = append(notes, result.note)

			continue
		}

		tools = append(tools, result.tools...)
	}

	if len(notes) == len(servers) {
		return nil, notes, fmt.Errorf("list mcp tools: %s", strings.Join(notes, "; "))
	}

	return tools, notes, nil
}

func callExecute(ctx context.Context, registry mcpclient.Registry, permissions PermissionSet, visibleServers []string, retainedRel string, raw json.RawMessage) (result ToolResult, err error) {
	var params executeParams
	if err := decodeToolParams(raw, &params); err != nil {
		return ToolResult{}, fmt.Errorf("parse execute params: %w", err)
	}

	params.Code = strings.TrimSpace(params.Code)
	if params.Code == "" {
		return ToolResult{}, errors.New("code is required")
	}

	names, errNames := codemode.FreeNames(params.Code)
	if errNames != nil {
		return ToolResult{}, fmt.Errorf("run code mode: %w", errNames)
	}

	var usedServers []string

	switch {
	case slices.Contains(names, searchBuiltinName):
		usedServers = visibleServers
	case len(names) > 0:
		for _, server := range visibleServers {
			prefix := codemode.SanitizeTool(server) + "_"
			if slices.ContainsFunc(names, func(name string) bool { return strings.HasPrefix(name, prefix) }) {
				usedServers = append(usedServers, server)
			}
		}
	}

	mcpCall := codemode.CallFunc(func(context.Context, string, string, map[string]any) (starlark.Value, error) {
		return nil, errors.New("mcp call unavailable")
	})

	var (
		mcpTools []codemode.ToolDesc
		notes    []string
	)

	if len(usedServers) > 0 {
		session := registry.Open()
		defer func() {
			if cerr := session.Close(); cerr != nil && err != nil {
				err = errors.Join(err, cerr)
			}
		}()

		var errList error

		mcpTools, notes, errList = listAllowedTools(ctx, session, permissions, usedServers)
		if errList != nil {
			return ToolResult{}, errList
		}

		mcpCall = func(ctx context.Context, server, name string, args map[string]any) (starlark.Value, error) {
			result, err := recordHostCall(ctx, server+"."+name, args, false, mcpHostCall{session: session, server: server, name: name, args: args})
			if err != nil {
				return nil, err
			}

			blocks, ok := result.Data.(*mcp.CallToolResult)
			if !ok {
				blocks = &mcp.CallToolResult{}
			}

			return newMCPResult(server+"."+name, result.Output, blocks)
		}
	}

	host, hostMap := codeModeHostToolsFromContext(ctx)

	host = append(host, codeModeSearchTool(buildCodeModeSearchIndex(hostMap, mcpTools)))

	value, errRun := codemode.Run(ctx, params.Code, mcpTools,
		func(ctx context.Context, subject string, args map[string]any) error {
			errCheck := CheckNestedPermission(ctx, executeToolName, mcpPermissionBucket, subject, args)
			if errCheck == nil {
				return nil
			}

			raw, _ := json.Marshal(args)
			tc, _ := toolCallContextFrom(ctx)
			record := codeModeCallRecord(ctx, subject, raw)

			if ctx.Err() != nil {
				record.cancel()
			} else {
				record.finish(ToolResult{}, errCheck)
			}

			return errors.Join(errCheck, tc.observations.recordCodeModeCall(ctx, &record, false))
		},
		mcpCall,
		host,
	)

	var (
		text   string
		files  []returnedFile
		failed bool
	)

	if errRun == nil {
		suffix := ""
		if len(notes) > 0 {
			suffix = "\n\nPartial MCP list failures:\n- " + strings.Join(notes, "\n- ")
		}

		text, files, failed, errRun = convertExecuteReturn(value, suffix)
	}

	if errRun != nil {
		if len(notes) > 0 {
			return ToolResult{}, fmt.Errorf("run code mode: %w (partial list failures: %s)", errRun, strings.Join(notes, "; "))
		}

		return ToolResult{}, fmt.Errorf("run code mode: %w", errRun)
	}

	tc, _ := toolCallContextFrom(ctx)

	var errSave error

	if tc.sink != nil {
		tc.sink.mu.Lock()
		tc.sink.settled = true
		tc.sink.mu.Unlock()
	}

	if backgroundDetached(ctx) {
		result, errSave = retainExecuteResult(tc.looper.promptExpansion.root, retainedRel, text, files)
	} else {
		result, errSave = tc.looper.saveExecuteResult(text, files)
	}

	if errSave != nil {
		return ToolResult{}, errSave
	}

	if failed {
		return result, toolResultError{result: result}
	}

	return result, nil
}

const defaultSearchLimit = 10

func codeModeSearchTool(index []codeModeSearchEntry) codemode.HostTool {
	return codemode.HostTool{
		Name:    searchBuiltinName,
		Details: codemode.NoDetails{},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":     map[string]any{"type": "string", "description": "Optional search text over path, description, and parameter names."},
				"namespace": map[string]any{"type": "string", "description": "Optional path prefix filter (host tool name, or MCP server name)."},
				"server":    map[string]any{"type": "string", "description": "Alias for namespace (MCP server)."},
				"offset":    map[string]any{"type": "integer", "description": "Pagination offset (default 0)."},
				"limit":     map[string]any{"type": "integer", "description": "Max results (default 10)."},
			},
		},
		CallValue: func(_ context.Context, args map[string]any) (starlark.Value, error) {
			return starlark.String(codeModeSearch(index, args)), nil
		},
	}
}

type codeModeSearchItem struct {
	Path        string `json:"path"`
	Description string `json:"description"`
	Signature   string `json:"signature"`
	Callable    string `json:"callable"`
}

type codeModeSearchResult struct {
	Items     []codeModeSearchItem `json:"items"`
	Remaining int                  `json:"remaining"`
	Next      *struct {
		Offset int `json:"offset"`
	} `json:"next"`
}

type codeModeSearchEntry struct {
	item       codeModeSearchItem
	searchText string
	score      int
}

func codeModeSearch(index []codeModeSearchEntry, args map[string]any) string {
	query := strings.TrimSpace(argString(args, "query"))
	namespace := cmp.Or(strings.TrimSpace(argString(args, "namespace")), strings.TrimSpace(argString(args, "server")))

	limit := argInt(args, "limit", defaultSearchLimit)
	if limit <= 0 {
		limit = defaultSearchLimit
	}

	scoped := index
	if namespace != "" {
		scoped = make([]codeModeSearchEntry, 0, len(index))
		for _, entry := range index {
			if entry.item.Path == namespace || strings.HasPrefix(entry.item.Path, namespace+".") {
				scoped = append(scoped, entry)
			}
		}
	}

	pathQuery := strings.TrimPrefix(query, "tools.")

	var ranked []codeModeSearchEntry

	if i := slices.IndexFunc(scoped, func(entry codeModeSearchEntry) bool {
		return pathQuery != "" && (entry.item.Path == pathQuery || entry.item.Callable == pathQuery)
	}); i >= 0 {
		ranked = scoped[i : i+1]
	} else {
		terms := searchTokenize(query)
		for _, entry := range scoped {
			score := openCodeSearchScore(terms, entry.item.Path, entry.item.Description, entry.searchText)
			if len(terms) > 0 && score == 0 {
				continue
			}

			entry.score = score
			ranked = append(ranked, entry)
		}

		slices.SortFunc(ranked, func(a, b codeModeSearchEntry) int {
			return cmp.Or(cmp.Compare(b.score, a.score), strings.Compare(a.item.Path, b.item.Path))
		})
	}

	offset := min(max(argInt(args, "offset", 0), 0), len(ranked))
	end := min(offset+limit, len(ranked))
	page := ranked[offset:end]
	remaining := len(ranked) - end

	out := codeModeSearchResult{
		Items:     make([]codeModeSearchItem, 0, len(page)),
		Remaining: remaining,
	}
	for _, entry := range page {
		out.Items = append(out.Items, entry.item)
	}

	if remaining > 0 {
		out.Next = &struct {
			Offset int `json:"offset"`
		}{Offset: end}
	}

	raw, _ := json.Marshal(out)

	return string(raw)
}

func buildCodeModeSearchIndex(hosts map[string]looperTool, mcpTools []codemode.ToolDesc) []codeModeSearchEntry {
	type row struct {
		name, sig, desc string
	}

	defN := codemode.DefaultConcurrency
	maxN := codemode.MaxConcurrency
	rows := []row{
		{"gather", fmt.Sprintf("gather([lambda: …, …], concurrency=%d)", defN), fmt.Sprintf("Run a list of zero-arg lambdas concurrently (not varargs); every result in order, failures included, a crashing branch as an error result (default concurrency %d, max %d). Example: gather([lambda: read(filePath=\"a\"), lambda: read(filePath=\"b\")], concurrency=%d).", defN, maxN, defN)},
		{"try_gather", fmt.Sprintf("try_gather([lambda: …, …], concurrency=%d)", defN), fmt.Sprintf("Like gather, but the first failure (a result with .ok False) or crash cancels the rest and is returned; otherwise the ordered results as a list that also has .ok/.unwrap() (default concurrency %d, max %d).", defN, maxN)},
		{"map", fmt.Sprintf("map(items, fn, concurrency=%d)", defN), fmt.Sprintf("Concurrent map over items; fn is one callable; every result in order, failures included, a crashing call as an error result (default concurrency %d, max %d).", defN, maxN)},
		{"try_map", fmt.Sprintf("try_map(items, fn, concurrency=%d)", defN), fmt.Sprintf("Like map, but the first failure or crash cancels the rest and is returned; otherwise the ordered results as a list that also has .ok/.unwrap() (default concurrency %d, max %d).", defN, maxN)},
		{"race", fmt.Sprintf("race([lambda: …, …], concurrency=%d)", defN), fmt.Sprintf("First successful zero-arg lambda in a list wins (not varargs); failures and crashes are losses; losers cancelled; if every branch fails, the first failure is returned (default concurrency %d, max %d).", defN, maxN)},
		{"race_first", fmt.Sprintf("race_first([lambda: …, …], concurrency=%d)", defN), fmt.Sprintf("First finished zero-arg lambda in a list wins (success, failure, or a crash as an error result; not varargs); losers cancelled (default concurrency %d, max %d).", defN, maxN)},
	}

	entries := make([]codeModeSearchEntry, 0, len(hosts)+len(mcpTools)+len(rows))
	for _, row := range rows {
		entries = append(entries, codeModeSearchEntry{
			item: codeModeSearchItem{
				Path:        row.name,
				Description: row.desc,
				Signature:   row.sig,
				Callable:    row.name,
			},
			searchText: strings.ToLower(strings.Join([]string{row.name, row.sig, row.desc, "concurrency", "parallel", "fanout"}, "\n")),
		})
	}

	for _, name := range slices.Sorted(maps.Keys(hosts)) {
		tool := hosts[name]
		schema := codeModeHostInputSchema(name, &tool.Definition)
		desc := tool.Definition.Description.Value
		sig := hostToolSignature(name, schema)
		entries = append(entries, codeModeSearchEntry{
			item: codeModeSearchItem{
				Path:        name,
				Description: desc,
				Signature:   sig,
				Callable:    name,
			},
			searchText: strings.ToLower(strings.Join([]string{name, desc, schemaSearchText(schema)}, "\n")),
		})
	}

	for _, tool := range mcpTools {
		callable := codemode.StarlarkName(tool.Server, tool.Name)
		path := tool.Server + "." + tool.Name
		sig := hostToolSignature(callable, tool.InputSchema)
		entries = append(entries, codeModeSearchEntry{
			item: codeModeSearchItem{
				Path:        path,
				Description: tool.Description,
				Signature:   sig,
				Callable:    callable,
			},
			searchText: strings.ToLower(strings.Join([]string{path, callable, tool.Name, tool.Description, schemaSearchText(tool.InputSchema)}, "\n")),
		})
	}

	return entries
}

func hostToolSignature(name string, schema map[string]any) string {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return name + "()"
	}

	keys := slices.Sorted(maps.Keys(props))

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"=...")
	}

	return name + "(" + strings.Join(parts, ", ") + ")"
}

func schemaSearchText(schema map[string]any) string {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return ""
	}

	parts := make([]string, 0, len(props)*2)
	for _, key := range slices.Sorted(maps.Keys(props)) {
		parts = append(parts, key)
		if prop, ok := props[key].(map[string]any); ok {
			if d, ok := prop["description"].(string); ok {
				parts = append(parts, d)
			}
		}
	}

	return strings.Join(parts, "\n")
}

func openCodeSearchScore(terms [][]string, path, description, searchText string) int {
	if len(terms) == 0 {
		return 1
	}

	path = strings.ToLower(path)
	description = strings.ToLower(description)
	score := 0

	for _, forms := range terms {
		if slices.ContainsFunc(forms, func(form string) bool {
			return path == form || strings.HasSuffix(path, "."+form)
		}) {
			score += 20
		}

		if slices.ContainsFunc(forms, func(form string) bool { return strings.Contains(path, form) }) {
			score += 8
		}

		if slices.ContainsFunc(forms, func(form string) bool { return strings.Contains(description, form) }) {
			score += 4
		}

		if slices.ContainsFunc(forms, func(form string) bool { return strings.Contains(searchText, form) }) {
			score += 2
		}
	}

	return score
}

func searchTokenize(query string) [][]string {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}

	var b strings.Builder

	for i, r := range query {
		if i > 0 && r >= 'A' && r <= 'Z' {
			prev := rune(query[i-1])
			if (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9') {
				b.WriteByte(' ')
			}
		}

		b.WriteRune(r)
	}

	raw := strings.ToLower(b.String())
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})

	var terms [][]string

	for _, term := range fields {
		forms := []string{term}
		if strings.HasSuffix(term, "es") && len(term) > 3 {
			forms = append(forms, strings.TrimSuffix(term, "es"))
		}

		if strings.HasSuffix(term, "s") && len(term) > 2 {
			forms = append(forms, strings.TrimSuffix(term, "s"))
		}

		terms = append(terms, forms)
	}

	return terms
}

func argString(args map[string]any, key string) string {
	v, _ := args[key].(string)

	return v
}

func argInt(args map[string]any, key string, fallback int) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}

	return fallback
}

func codeModeHostToolsFromContext(ctx context.Context) (host []codemode.HostTool, hosts map[string]looperTool) {
	tc, _ := toolCallContextFrom(ctx)

	hosts = tc.looper.CodeModeHosts
	names := slices.Sorted(maps.Keys(hosts))
	host = make([]codemode.HostTool, 0, len(names))

	for _, toolName := range names {
		tool := hosts[toolName]

		host = append(host, codemode.HostTool{
			Name:        toolName,
			InputSchema: codeModeHostInputSchema(toolName, &tool.Definition),
			FileArgs:    map[string][]string{"bash": {"stdin"}, "decision": {"images[]"}}[toolName],
			Details:     codeModeCallDetails(toolName),
			CallValue: func(ctx context.Context, args map[string]any) (starlark.Value, error) {
				result, errCall := recordHostCall(ctx, toolName, args, tool.resumable, toolHostCall{name: toolName, tool: &tool, output: tc.output})
				if errCall != nil {
					return nil, errCall
				}

				if value, ok := hostCallValue(result); ok {
					return value, nil
				}

				if toolName == "decision" {
					return starlark.Call(&starlark.Thread{Name: toolName}, starjson.Module.Members["decode"], starlark.Tuple{starlark.String(result.Output)}, nil)
				}

				return starlark.String(result.Output), nil
			},
		})
	}

	return host, hosts
}

func hostCallValue(result ToolResult) (starlark.Value, bool) {
	switch data := result.Data.(type) {
	case starlark.Value:
		return data, true
	case BashResult:
		return newBashStarlarkResult(data.Output, data.ErrorCode), true
	case webFetchPage:
		return webFetchValue(&data), true
	}

	return nil, false
}

var errHostCallInterrupted = errors.New("tool call aborted because the runtime stopped before the call completed; side effects may have partially occurred, so inspect the current environment before deciding whether to retry or continue")

type hostCall interface {
	run(context.Context, json.RawMessage) (ToolResult, error)
}

type mcpHostCall struct {
	session      *mcpclient.Session
	server, name string
	args         map[string]any
}

func (c mcpHostCall) run(ctx context.Context, _ json.RawMessage) (ToolResult, error) {
	result, text, err := c.session.CallTool(ctx, c.server, c.name, c.args)
	if err != nil {
		return ToolResult{}, fmt.Errorf("%w", err)
	}

	return ToolResult{Output: text, Data: result}, nil
}

type toolHostCall struct {
	name   string
	tool   *looperTool
	output chan<- ChatResponse
}

func (c toolHostCall) run(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	if err := CheckNestedToolCall(ctx, c.name, c.tool, raw); err != nil {
		return ToolResult{}, err
	}

	return c.tool.Call(ctx, raw, c.output, toolCallMetadata{})
}

func recordHostCall(ctx context.Context, name string, args map[string]any, resumable bool, call hostCall) (ToolResult, error) {
	tc, _ := toolCallContextFrom(ctx)

	raw, _ := json.Marshal(args)

	sum := sha256.Sum256(raw)
	key := ToolCallKey(ctx)
	want := hostCallStep{Name: name, Hash: hex.EncodeToString(sum[:])}
	record := codeModeCallRecord(ctx, name, raw)

	var step hostCallStep

	found, err := loadStep(ctx, tc.looper.Journal, key, &step)
	if err != nil {
		return ToolResult{}, err
	}

	if found && step.Name == want.Name && step.Hash == want.Hash {
		switch {
		case step.Err != "":
			errStep := errors.New(step.Err)

			return ToolResult{}, errors.Join(errStep, tc.observations.recordCodeModeCall(ctx, record.finish(ToolResult{}, errStep), true))
		case step.Done:
			result := TextToolResult(step.Output)
			switch {
			case step.Bash != nil:
				result.Data = *step.Bash
			case step.WebFetch != nil:
				result.Data = *step.WebFetch
			case step.MCP != nil:
				result.Data = step.MCP
			}

			if step.Typed != nil {
				var errReplay error
				if result, errReplay = step.Typed.replay(ctx, step.Output, call, raw); errReplay != nil {
					return ToolResult{}, errors.Join(errReplay, tc.observations.recordCodeModeCall(ctx, record.finish(ToolResult{}, errReplay), true))
				}
			}

			return result, tc.observations.recordCodeModeCall(ctx, record.finish(result, nil), true)
		case !resumable:
			return ToolResult{}, errors.Join(errHostCallInterrupted, tc.observations.recordCodeModeCall(ctx, record.finish(ToolResult{}, errHostCallInterrupted), true))
		}
	}

	if err := tc.observations.recordCodeModeCall(ctx, &record, false); err != nil {
		return ToolResult{}, err
	}

	if err := saveStep(ctx, tc.looper.Journal, key, &want); err != nil {
		return ToolResult{}, err
	}

	runCtx, stop := callContext(ctx, resumable)
	defer stop()

	result, errRun := call.run(runCtx, raw)
	if errRun == nil && runCtx.Err() != nil {
		errRun = fmt.Errorf("%s stopped: %w", name, context.Cause(runCtx))
	}

	if output := tc.looper.turnSecrets.Redact(result.Output); output != result.Output {
		bash, ok := result.Data.(BashResult)
		result.Output, result.Data, bash.Output = output, nil, output

		if ok {
			result.Data = bash
		}
	}

	if errRun != nil && tc.looper.turnSecrets.Redact(errRun.Error()) != errRun.Error() {
		errRun = errors.New(tc.looper.turnSecrets.Redact(errRun.Error()))
	}

	if errRun != nil && runCtx.Err() != nil {
		return result, errors.Join(errRun, tc.observations.recordCodeModeCall(ctx, record.cancel(), false))
	}

	want.Done, want.Output = true, result.Output
	switch data := result.Data.(type) {
	case BashResult:
		want.Bash = &data
	case webFetchPage:
		want.WebFetch = &data
	case *mcp.CallToolResult:
		want.MCP = data
	default:
		want.Typed = typedHostStepOf(data)
	}

	if errRun != nil {
		want.Err = errRun.Error()
	}

	return result, errors.Join(errRun, saveStep(ctx, tc.looper.Journal, key, &want), tc.observations.recordCodeModeCall(ctx, record.finish(result, errRun), false))
}

func codeModeHostInputSchema(name string, def *responses.FunctionToolParam) map[string]any {
	props, _ := def.Parameters["properties"].(map[string]any)
	if props == nil {
		return maps.Clone(def.Parameters)
	}

	return map[string]any{
		"type":                 "object",
		"properties":           maps.Clone(props),
		"additionalProperties": false,
		"required":             codeModeHostRequiredFields(name, def.Parameters),
	}
}

func codeModeHostRequiredFields(name string, params map[string]any) []string {
	switch name {
	case "bash":
		return []string{"command"}
	case "apply_patch":
		return []string{"patchText"}
	case "glob", "grep":
		return []string{"pattern"}
	case "webfetch":
		return []string{"url"}
	case "read":
		return []string{"filePath"}
	case "decision":
		return []string{"model", "state", "questions"}
	}

	required, _ := params["required"].([]string)

	return slices.Clone(required)
}
