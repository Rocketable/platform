package rocketcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Rocketable/platform/internal/rocketcode/codemode"
	"github.com/Rocketable/platform/internal/rocketcode/mcpclient"
	"github.com/openai/openai-go/v3/responses"
	"go.starlark.net/starlark"
	"golang.org/x/sync/errgroup"
)

const (
	executeToolName     = "execute"
	searchBuiltinName   = "search"
	mcpPermissionBucket = "mcp"
	// executeNestedToolPrefix marks nested code-mode tool diagnostics for thinking UI.
	executeNestedToolPrefix = executeToolName + " → "
	codeModeRawStringRule   = `Use raw strings (r"..." or r'''...''') for shell, regex, and paths. Ordinary "..." rejects unknown escapes such as \( \. \$.`
)

func newMCPRegistry(workspace string, servers map[string]mcpclient.ServerConfig) (*mcpclient.Registry, error) {
	if len(servers) == 0 {
		return nil, nil
	}

	registry, err := mcpclient.New(workspace, servers)
	if err != nil {
		return nil, fmt.Errorf("create mcp registry: %w", err)
	}

	return registry, nil
}

func ruleMatchesMCPServer(pattern, server string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || pattern == "*" {
		return true
	}

	name, _, ok := strings.Cut(pattern, ".")
	if !ok {
		return name == server
	}

	return name == server || name == "*"
}

func eachMCPAllowRule(permissions PermissionSet, fn func(pattern string) bool) {
	for _, bucket := range permissions.Buckets {
		if bucket.Name != mcpPermissionBucket {
			continue
		}

		for _, rule := range bucket.Rules {
			if rule.Action != PermissionAllow && rule.Action != PermissionAuto {
				continue
			}

			if fn(rule.Pattern) {
				return
			}
		}
	}
}

func visibleMCPServers(permissions PermissionSet, servers []string) []string {
	var visible []string

	for _, server := range servers {
		match := false

		eachMCPAllowRule(permissions, func(pattern string) bool {
			if ruleMatchesMCPServer(pattern, server) {
				match = true

				return true
			}

			return false
		})

		if match {
			visible = append(visible, server)
		}
	}

	return visible
}

func mcpVisibilitySubjects(permissions PermissionSet, servers []string) []string {
	if len(servers) == 0 {
		return nil
	}

	seen := make(map[string]struct{})

	var subjects []string

	eachMCPAllowRule(permissions, func(pattern string) bool {
		for _, server := range servers {
			if !ruleMatchesMCPServer(pattern, server) {
				continue
			}

			subject := strings.TrimSpace(pattern)
			if subject == "" {
				subject = "*"
			}

			if _, ok := seen[subject]; ok {
				return false
			}

			seen[subject] = struct{}{}
			subjects = append(subjects, subject)

			return false
		}

		return false
	})

	slices.Sort(subjects)

	return subjects
}

func mcpToolAllowed(permissions PermissionSet, server, tool string) bool {
	action, _ := permissions.Evaluate(mcpPermissionBucket, server+"."+tool)

	return action == PermissionAllow || action == PermissionAuto
}

// executeDescription is short invariant guidance. Catalog lives in the system prompt;
// full discovery is in-script search (OpenCode-style).
func executeDescription() string {
	defN := codemode.DefaultConcurrency
	maxN := codemode.MaxConcurrency

	return strings.Join([]string{
		"Run Starlark in Code Mode to orchestrate tool calls and compose their results.",
		"Define def main() that returns a string (or JSON-encodable value). Only keyword arguments are allowed on tool calls.",
		"No import/from, threads, concurrent.futures, or stdlib. Use only the builtins listed here and in search.",
		codeModeRawStringRule,
		"Call host tools by name (read, bash, …), platform/custom tools by name, and MCP as server_toolname(**kwargs).",
		fmt.Sprintf("Concurrency only via gather/map/race/race_first (default concurrency=%d, max %d). callables is one list of zero-arg lambdas, not varargs.", defN, maxN),
		fmt.Sprintf("Example: gather([lambda: read(filePath=\"a\"), lambda: read(filePath=\"b\")], concurrency=%d)", defN),
		"Use search(query=\"\", namespace=\"\", offset=0, limit=10) inside the script to discover tools and concurrency builtins (path, description, signature).",
		"A short Code Mode catalog is also in the system prompt; search when you need more detail or MCP schemas.",
	}, "\n")
}

// codeModeSystemPrompt is appended to the system prompt when execute is available
// (same channel as permissionPrompt).
func codeModeSystemPrompt(hosts map[string]looperTool, servers []string) string {
	defN := codemode.DefaultConcurrency
	maxN := codemode.MaxConcurrency

	lines := []string{
		"## Code Mode",
		"",
		"Use the execute tool with a short Starlark script (def main() returning a string).",
		"No import/from, threads, concurrent.futures, or stdlib.",
		codeModeRawStringRule,
		"Inside the script, call tools with keyword arguments only.",
		"Discover tools with search(query=\"\", namespace=\"\", offset=0, limit=10).",
		"search returns JSON: {items:[{path,description,signature}], remaining, next}.",
		"",
	}

	if len(hosts) > 0 {
		lines = append(lines, "Host tools:")

		for _, name := range slices.Sorted(maps.Keys(hosts)) {
			tool := hosts[name]

			desc := strings.TrimSpace(strings.SplitN(tool.Definition.Description.Value, "\n", 2)[0])
			if len(desc) > 120 {
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
		lines = append(lines,
			"MCP namespaces: "+strings.Join(servers, ", ")+".",
			"Call MCP as server_toolname(**kwargs). Use search(namespace=\"server\") for schemas.",
			"",
		)
	}

	lines = append(lines,
		"Concurrency (callables is one list of zero-arg lambdas, not varargs):",
		fmt.Sprintf("- gather([lambda: …, …], concurrency=%d) // run together; ordered results; fail-fast cancels siblings", defN),
		fmt.Sprintf("- map(items, fn, concurrency=%d) // concurrent map; ordered results; fail-fast cancels siblings", defN),
		fmt.Sprintf("- race([lambda: …, …], concurrency=%d) // first success wins; losers cancelled", defN),
		fmt.Sprintf("- race_first([lambda: …, …], concurrency=%d) // first finish (ok or err) wins; losers cancelled", defN),
		fmt.Sprintf("Example: gather([lambda: read(filePath=\"a\"), lambda: read(filePath=\"b\")], concurrency=%d)", defN),
		fmt.Sprintf("Default concurrency %d, max %d. Nested gather/map/race allowed. search(query=\"gather\") for details.", defN, maxN),
	)

	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func withCodeModeSystemPrompt(base string, modelTools, hosts map[string]looperTool, servers []string) string {
	if _, ok := modelTools[executeToolName]; !ok {
		return base
	}

	extra := codeModeSystemPrompt(hosts, servers)
	if extra == "" {
		return base
	}

	return strings.TrimSpace(base + "\n\n" + extra)
}

func (f *toolFactory) mcpToolsFor(agent *Agent, codeHosts map[string]looperTool) map[string]looperTool {
	if agent == nil {
		return nil
	}

	var servers []string
	if f.mcpRegistry != nil {
		servers = visibleMCPServers(agent.Permission, f.mcpRegistry.Names())
	}

	mcpSubjects := mcpVisibilitySubjects(agent.Permission, servers)
	if len(mcpSubjects) == 0 && len(codeHosts) == 0 {
		return nil
	}

	entryPermission, entrySubjects := codeModeRunEntryGate(agent, mcpSubjects, codeHosts)
	entrySubjectsCopy := slices.Clone(entrySubjects)
	registry := f.mcpRegistry
	permissions := agent.Permission
	serversCopy := slices.Clone(servers)

	return map[string]looperTool{
		executeToolName: {
			Definition: *functionTool(executeToolName, executeDescription(), map[string]any{
				"code": map[string]any{
					"type":        "string",
					"description": fmt.Sprintf("Starlark source defining def main() that returns a string (or JSON-encodable value). %s Concurrency: gather([lambda: …], concurrency=%d) — one list of zero-arg lambdas, not varargs.", codeModeRawStringRule, codemode.DefaultConcurrency),
				},
			}),
			Permission:         entryPermission,
			VisibilitySubjects: entrySubjectsCopy,
			Subjects:           func(json.RawMessage) ([]string, error) { return entrySubjectsCopy, nil },
			Call: func(ctx context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
				return callExecute(ctx, registry, permissions, serversCopy, raw)
			},
		},
	}
}

func codeModeRunEntryGate(agent *Agent, mcpSubjects []string, codeHosts map[string]looperTool) (permission string, subjects []string) {
	if len(mcpSubjects) > 0 {
		return mcpPermissionBucket, mcpSubjects
	}

	// Prefer sandbox host buckets so skill/task auto rules do not gate execute entry.
	hostBuckets := []string{"read", "edit", "bash", "glob", "grep", "webfetch"}
	for _, want := range hostBuckets {
		if subj, ok := firstRuleSubject(agent.Permission, want, PermissionAllow); ok {
			return want, []string{subj}
		}
	}

	for _, want := range hostBuckets {
		if subj, ok := firstRuleSubject(agent.Permission, want, PermissionAuto); ok {
			return want, []string{subj}
		}
	}

	// Custom/platform tools (rocketclaw_*, ask_user_question, …) may be the only code hosts.
	for _, name := range slices.Sorted(maps.Keys(codeHosts)) {
		tool := codeHosts[name]

		perm := tool.Permission
		if perm == "" {
			perm = name
		}

		for _, subject := range tool.VisibilitySubjects {
			action := agent.Permission.evaluate(perm, subject).Action
			if action == permissionAllow || action == permissionAuto {
				return perm, []string{subject}
			}
		}

		if subj, ok := firstRuleSubject(agent.Permission, perm, PermissionAllow); ok {
			return perm, []string{subj}
		}

		if subj, ok := firstRuleSubject(agent.Permission, perm, PermissionAuto); ok {
			return perm, []string{subj}
		}
	}

	// execute is only registered when codeHosts is non-empty; keep a stable host bucket.
	return "read", []string{"code_mode"}
}

func firstRuleSubject(permissions PermissionSet, bucket string, action PermissionAction) (string, bool) {
	for _, b := range permissions.Buckets {
		if b.Name != bucket {
			continue
		}

		for _, rule := range b.Rules {
			if rule.Action == action {
				return subjectMatchingPattern(rule.Pattern), true
			}
		}
	}

	return "", false
}

func subjectMatchingPattern(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || pattern == "*" {
		return "code_mode"
	}

	var b strings.Builder

	for _, r := range pattern {
		switch r {
		case '*', '?':
			b.WriteByte('x')
		default:
			b.WriteRune(r)
		}
	}

	if b.Len() == 0 {
		return "code_mode"
	}

	return b.String()
}

type executeParams struct {
	Code string `json:"code"`
}

func listAllowedTools(ctx context.Context, session *mcpclient.Session, permissions PermissionSet, servers []string) ([]codemode.ToolDesc, []string, error) {
	if len(servers) == 0 {
		return nil, nil, nil
	}

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
				if !mcpToolAllowed(permissions, info.Server, info.Name) {
					continue
				}

				results[i].tools = append(results[i].tools, codemode.ToolDesc{
					Server:      info.Server,
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
		tools  []codemode.ToolDesc
		notes  []string
		failed int
	)

	for _, result := range results {
		if result.note != "" {
			failed++

			notes = append(notes, result.note)

			continue
		}

		tools = append(tools, result.tools...)
	}

	if failed == len(servers) {
		return nil, notes, fmt.Errorf("list mcp tools: %s", strings.Join(notes, "; "))
	}

	return tools, notes, nil
}

func callExecute(ctx context.Context, registry *mcpclient.Registry, permissions PermissionSet, visibleServers []string, raw json.RawMessage) (result ToolResult, err error) {
	var params executeParams
	if err := decodeToolParams(raw, &params); err != nil {
		return ToolResult{}, fmt.Errorf("parse execute params: %w", err)
	}

	params.Code = strings.TrimSpace(params.Code)
	if params.Code == "" {
		return ToolResult{}, errors.New("code is required")
	}

	mcpCall := codemode.CallFunc(func(context.Context, string, string, map[string]any) (string, error) {
		return "", errors.New("mcp call unavailable")
	})

	var (
		mcpTools []codemode.ToolDesc
		notes    []string
	)

	if registry != nil && len(visibleServers) > 0 {
		session := registry.Open()
		defer func() {
			// Close failures must not discard a successful script result.
			if cerr := session.Close(); cerr != nil && err != nil {
				err = errors.Join(err, cerr)
			}
		}()

		var errList error

		mcpTools, notes, errList = listAllowedTools(ctx, session, permissions, visibleServers)
		if errList != nil {
			return ToolResult{}, errList
		}

		mcpCall = session.CallTool
	}

	host, hostMap := codeModeHostToolsFromContext(ctx)

	host = append(host, codeModeSearchTool(buildCodeModeSearchIndex(hostMap, mcpTools)))

	out, errRun := codemode.Run(ctx, params.Code, mcpTools,
		func(ctx context.Context, subject string, args map[string]any) error {
			return CheckNestedPermission(ctx, executeToolName, mcpPermissionBucket, subject, args)
		},
		mcpCall,
		emitNestedExecuteToolDiagnostic,
		host,
	)
	if errRun != nil {
		if len(notes) > 0 {
			return ToolResult{}, fmt.Errorf("run code mode: %w (partial list failures: %s)", errRun, strings.Join(notes, "; "))
		}

		return ToolResult{}, fmt.Errorf("run code mode: %w", errRun)
	}

	if len(notes) > 0 {
		out += "\n\nPartial MCP list failures:\n- " + strings.Join(notes, "\n- ")
	}

	return TextToolResult(out), nil
}

// defaultSearchLimit matches OpenCode packages/codemode/src/tool-runtime.ts.
const defaultSearchLimit = 10

// codeModeSearchTool mirrors OpenCode search: query, namespace, offset, limit →
// {items:[{path,description,signature}], remaining, next}.
// Upstream: packages/codemode/src/tool-runtime.ts (makeSearchTool).
func codeModeSearchTool(index []codeModeSearchEntry) codemode.HostTool {
	return codemode.HostTool{
		Name: searchBuiltinName,
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
		Call: func(_ context.Context, args map[string]any) (string, error) {
			return codeModeSearch(index, args), nil
		},
	}
}

type codeModeSearchItem struct {
	Path        string `json:"path"`
	Description string `json:"description"`
	Signature   string `json:"signature"`
	// Callable is the Starlark builtin name (host name or server_tool).
	Callable string `json:"callable"`
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

	namespace := strings.TrimSpace(argString(args, "namespace"))
	if namespace == "" {
		namespace = strings.TrimSpace(argString(args, "server"))
	}

	offset := argInt(args, "offset", 0)
	limit := argInt(args, "limit", defaultSearchLimit)

	if offset < 0 {
		offset = 0
	}

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

	// OpenCode: exact path match short-circuits ranking.
	pathQuery := strings.TrimPrefix(query, "tools.")

	var ranked []codeModeSearchEntry

	if pathQuery != "" {
		for _, entry := range scoped {
			if entry.item.Path == pathQuery || entry.item.Callable == pathQuery {
				ranked = []codeModeSearchEntry{entry}

				break
			}
		}
	}

	if ranked == nil {
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
			if a.score != b.score {
				return b.score - a.score
			}

			return strings.Compare(a.item.Path, b.item.Path)
		})
	}

	if offset > len(ranked) {
		offset = len(ranked)
	}

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

	raw, err := json.Marshal(out)
	if err != nil {
		return `{"items":[],"remaining":0,"next":null}`
	}

	return string(raw)
}

func concurrencySearchEntries() []codeModeSearchEntry {
	type row struct {
		name, sig, desc string
	}

	defN := codemode.DefaultConcurrency
	maxN := codemode.MaxConcurrency
	rows := []row{
		{"gather", fmt.Sprintf("gather([lambda: …, …], concurrency=%d)", defN), fmt.Sprintf("Run a list of zero-arg lambdas concurrently (not varargs); ordered results; fail-fast cancels siblings (default concurrency %d, max %d). Example: gather([lambda: read(filePath=\"a\"), lambda: read(filePath=\"b\")], concurrency=%d).", defN, maxN, defN)},
		{"map", fmt.Sprintf("map(items, fn, concurrency=%d)", defN), fmt.Sprintf("Concurrent map over items; fn is one callable; ordered results; fail-fast cancels siblings (default concurrency %d, max %d).", defN, maxN)},
		{"race", fmt.Sprintf("race([lambda: …, …], concurrency=%d)", defN), fmt.Sprintf("First successful zero-arg lambda in a list wins (not varargs); losers cancelled (default concurrency %d, max %d).", defN, maxN)},
		{"race_first", fmt.Sprintf("race_first([lambda: …, …], concurrency=%d)", defN), fmt.Sprintf("First finished zero-arg lambda in a list wins (success or failure; not varargs); losers cancelled (default concurrency %d, max %d).", defN, maxN)},
	}

	entries := make([]codeModeSearchEntry, 0, len(rows))
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

	return entries
}

func buildCodeModeSearchIndex(hosts map[string]looperTool, mcp []codemode.ToolDesc) []codeModeSearchEntry {
	entries := make([]codeModeSearchEntry, 0, len(hosts)+len(mcp)+4)
	entries = append(entries, concurrencySearchEntries()...)

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

	for _, tool := range mcp {
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
	if schema == nil {
		return ""
	}

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

// openCodeSearchScore mirrors packages/codemode/src/tool-runtime.ts scoring weights.
// path/description are lowercased here; searchText is already lowercased in the index.
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

// searchTokenize mirrors OpenCode tokenize + termForms.
func searchTokenize(query string) [][]string {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	// Split camelCase then non-alnum.
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
		if term == "" || term == "*" {
			continue
		}

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
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return fallback
	}
}

// codeModeHostToolsFromContext binds host tools from looper.CodeModeHosts.
func codeModeHostToolsFromContext(ctx context.Context) (host []codemode.HostTool, hosts map[string]looperTool) {
	tc, ok := toolCallContextFrom(ctx)
	if !ok {
		return nil, nil
	}

	hosts = tc.looper.CodeModeHosts
	names := slices.Sorted(maps.Keys(hosts))
	host = make([]codemode.HostTool, 0, len(names))

	for _, name := range names {
		tool := hosts[name]
		if tool.Call == nil {
			continue
		}

		schema := codeModeHostInputSchema(name, &tool.Definition)
		toolName := name
		toolCopy := tool
		output := tc.output

		callTool := func(ctx context.Context, args map[string]any) (ToolResult, error) {
			raw, err := json.Marshal(args)
			if err != nil {
				return ToolResult{}, fmt.Errorf("marshal %s args: %w", toolName, err)
			}

			if len(raw) == 0 {
				raw = json.RawMessage(`{}`)
			}

			if err := CheckNestedToolCall(ctx, toolName, &toolCopy, raw); err != nil {
				return ToolResult{}, err
			}

			return toolCopy.Call(ctx, raw, output, toolCallMetadata{})
		}

		bound := codemode.HostTool{
			Name:        toolName,
			InputSchema: schema,
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				result, errCall := callTool(ctx, args)
				if errCall != nil {
					return "", errCall
				}

				return attachmentOutputMessage(result), nil
			},
		}
		if toolName == "bash" {
			bound.CallValue = func(ctx context.Context, args map[string]any) (starlark.Value, error) {
				result, errCall := callTool(ctx, args)
				if errCall != nil {
					return nil, errCall
				}

				if bashResult, ok := result.Data.(BashResult); ok {
					return newBashStarlarkResult(bashResult), nil
				}

				return starlark.String(attachmentOutputMessage(result)), nil
			}
		}

		host = append(host, bound)
	}

	return host, hosts
}

// emitNestedExecuteToolDiagnostic reports a nested code-mode tool call into thinking traces.
// Uses emitChatResponse (blocking fallback) so nested steps are not dropped when the output
// buffer is full — unlike emitDiagnosticChatResponse, which drops under backpressure.
func emitNestedExecuteToolDiagnostic(ctx context.Context, path []string, nestedName string, args map[string]any) {
	tc, ok := toolCallContextFrom(ctx)
	if !ok || tc.looper == nil || !tc.looper.Diagnostics || tc.output == nil {
		return
	}

	raw, err := json.Marshal(args)
	if err != nil || len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}

	namePath := append(slices.Clone(path), nestedName)
	nestedName = strings.Join(namePath, " → ")

	emitChatResponse(tc.output, ChatResponse{
		Kind: ChatResponseAssistantTool,
		Tool: &ToolDiagnostic{
			Phase:     toolDiagnosticPhaseCall,
			Name:      executeNestedToolPrefix + nestedName,
			Arguments: raw,
		},
	})
}

func codeModeHostInputSchema(name string, def *responses.FunctionToolParam) map[string]any {
	if def == nil || def.Parameters == nil {
		return nil
	}

	params := maps.Clone(def.Parameters)

	props, _ := params["properties"].(map[string]any)
	if props == nil {
		return params
	}

	required := codeModeHostRequiredFields(name, params)

	return map[string]any{
		"type":                 "object",
		"properties":           maps.Clone(props),
		"additionalProperties": false,
		"required":             required,
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
		// filePath or filename accepted by Call; neither forced here.
		return nil
	default:
		return schemaRequiredStrings(params["required"])
	}
}

func schemaRequiredStrings(raw any) []string {
	switch required := raw.(type) {
	case []string:
		return slices.Clone(required)
	case []any:
		out := make([]string, 0, len(required))
		for _, item := range required {
			text, ok := item.(string)
			if !ok || text == "" {
				continue
			}

			out = append(out, text)
		}

		return out
	default:
		return nil
	}
}
