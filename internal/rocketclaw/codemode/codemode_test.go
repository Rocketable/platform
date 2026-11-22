package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go.starlark.net/starlark"
)

func allowAll(context.Context, string, map[string]any) error { return nil }

func runToString(ctx context.Context, source string, tools []ToolDesc, decide DecideFunc, call CallFunc, host []HostTool) (string, error) {
	value, err := Run(ctx, source, tools, decide, call, host)
	if err != nil {
		return "", err
	}

	if text, ok := value.(*TextValue); ok {
		return text.content, nil
	}

	if IsFailure(value) {
		return value.String(), nil
	}

	if text, ok := starlark.AsString(value); ok {
		return text, nil
	}

	return encodeJSON(value)
}

func TestRunRawStringKeepsUnknownEscapes(t *testing.T) {
	got, err := runToString(t.Context(), `def main():
    return r"find . \( -name '*.go' \)"
`, nil, allowAll, nilCall, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got != `find . \( -name '*.go' \)` {
		t.Fatalf("Run() = %q, want raw find expression", got)
	}
}

func TestRunParseFailureDoesNotCallHost(t *testing.T) {
	cases := []struct {
		name, source, message string
		category              parseCategory
	}{
		{
			name:     "raw single-line newline",
			source:   "def main():\n    return bash(command=r\"python3 - <<'PY'\nprint(\"hello\")\nPY\")\n",
			category: parseCategoryUnexpectedNewline,
			message:  "unexpected newline in string",
		},
		{
			name:     "unterminated string",
			source:   "def main():\n    return \"hello",
			category: parseCategoryUnterminatedString,
			message:  "unexpected EOF in string",
		},
		{
			name:     "invalid escape",
			source:   "def main():\n    return \"\\(\"\n",
			category: parseCategoryInvalidEscape,
			message:  "invalid escape sequence",
		},
		{
			name:     "exposed dollar",
			source:   "def main():\n    return \"foo\" $bar\n",
			category: parseCategoryUnexpectedCharacter,
			message:  "unexpected input character '$'",
		},
		{
			name:     "missing comma",
			source:   "def main():\n    return bash(command=\"a\" command=\"b\")\n",
			category: parseCategorySyntax,
			message:  "want ','",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false

			_, err := runToString(t.Context(), tc.source, nil, allowAll, nilCall, []HostTool{{
				Details: NoDetails{},
				Name:    "bash",
				CallValue: func(context.Context, map[string]any) (starlark.Value, error) {
					called = true
					return starlark.String("dispatched"), nil
				},
			}})
			if err == nil {
				t.Fatal("Run() error = nil, want parse diagnostic")
			}

			if called {
				t.Fatal("host tool dispatched on parse failure")
			}

			var got parseDiagnostic
			if errUnmarshal := json.Unmarshal([]byte(err.Error()), &got); errUnmarshal != nil {
				t.Fatalf("Run() error %q is not parse JSON: %v", err, errUnmarshal)
			}

			if got.Phase != "codemode_parse" || got.ExecutionStarted || got.Category != string(tc.category) {
				t.Fatalf("diagnostic %+v, want phase=codemode_parse execution_started=false category=%s", got, tc.category)
			}

			if !strings.Contains(got.Message, tc.message) || got.Line < 1 || got.Column < 1 || got.LikelyCause == "" || got.RecommendedFix == "" {
				t.Fatalf("diagnostic %+v, want message containing %q with line/column/cause/fix", got, tc.message)
			}
		})
	}
}

func TestRunRawTripleQuotedMultiline(t *testing.T) {
	var got string

	out, err := runToString(t.Context(), `def main():
    return bash(command=r'''python3 - <<'PY'
print("hello")
PY''')
`, nil, allowAll, nilCall, []HostTool{{
		Details: NoDetails{},
		Name:    "bash",
		CallValue: func(_ context.Context, args map[string]any) (starlark.Value, error) {
			got, _ = args["command"].(string)
			return starlark.String("ok"), nil
		},
	}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if out != "ok" || !strings.Contains(got, "print(\"hello\")") || !strings.Contains(got, "\n") {
		t.Fatalf("command = %q, result = %q", got, out)
	}
}

func TestRunDollarInsideRawString(t *testing.T) {
	got, err := runToString(t.Context(), `def main():
    return r'''rg -F -- "$pattern" file.txt'''
`, nil, allowAll, nilCall, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got != `rg -F -- "$pattern" file.txt` {
		t.Fatalf("Run() = %q", got)
	}
}

func TestRunMainReturnsDictJSON(t *testing.T) {
	got, err := runToString(t.Context(), `def main():
    return {"a": 1, "b": True}
`, nil, allowAll, nilCall, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got != `{"a":1,"b":true}` {
		t.Fatalf("Run() = %q, want JSON object", got)
	}
}

func TestRunJSONModule(t *testing.T) {
	got, err := runToString(t.Context(), `def main():
    data = json.decode('{"scores":[2,3]}')
    return json.encode({"total": data["scores"][0] + data["scores"][1]})
`, nil, allowAll, nilCall, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if want := `{"total":5}`; got != want {
		t.Fatalf("Run() = %q, want %q", got, want)
	}
}

func TestRunRejectsInvalidProgram(t *testing.T) {
	for _, tc := range []struct {
		name, source, want string
	}{
		{"None result", "def main():\n    return None\n", "main returned None"},
		{"missing main", "def other():\n    return 'unused'\n", "main is required"},
		{"noncallable main", "main = 1\n", "main must be a function"},
		{"initialization fails", "x = 1 // 0\ndef main():\n    return 'unused'\n", "execute codemode:"},
		{"undefined name", "def main():\n    return unknown\n", "execute codemode:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runToString(t.Context(), tc.source, nil, allowAll, nilCall, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Run() error = %v, want %q", err, tc.want)
			}

			if got != "" {
				t.Fatalf("Run() = %q, want no result for an invalid program", got)
			}
		})
	}
}

func TestRunDecideDenyDoesNotCall(t *testing.T) {
	called := false
	tools := []ToolDesc{{
		Server: "demo",
		Name:   "echo",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"message": map[string]any{"type": "string"}},
		},
	}}

	_, err := runToString(t.Context(), `def main():
    return demo_echo(message="hi").unwrap()
`, tools, func(context.Context, string, map[string]any) error {
		return errors.New("denied")
	}, func(context.Context, string, string, map[string]any) (starlark.Value, error) {
		called = true
		return starlark.String(""), nil
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("Run() error = %v, want denied", err)
	}

	if called {
		t.Fatal("call invoked after deny")
	}
}

func TestRunInvalidSchemaArgsDoesNotCall(t *testing.T) {
	for _, tc := range []struct {
		name, expression, want string
		schema                 map[string]any
	}{
		{
			name:       "invalid arguments",
			expression: `demo_echo(other="x")`,
			want:       "demo.echo: invalid arguments: validate args:",
			schema: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"message": map[string]any{"type": "string"}},
				"required":             []any{"message"},
				"additionalProperties": false,
			},
		},
		{"positional argument", `demo_echo("hi")`, "demo.echo: only keyword arguments are allowed; use keyword arguments", nil},
		{"positional argument naming the one required parameter", `demo_echo("hi")`, "demo.echo: only keyword arguments are allowed; call demo_echo(message=...)", map[string]any{"type": "object", "required": []any{"message"}}},
		{"unencodable argument", `demo_echo(message=lambda: "hi")`, "demo.echo: invalid arguments: encode kwargs:", nil},
		{"invalid schema type", `demo_echo()`, "parse input schema:", map[string]any{"type": 1}},
		{"unresolved schema reference", `demo_echo()`, "resolve input schema:", map[string]any{"$ref": "#/$defs/missing"}},
		{"unencodable schema", `demo_echo()`, "encode input schema:", map[string]any{"default": make(chan string)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			tools := []ToolDesc{{Server: "demo", Name: "echo", InputSchema: tc.schema}}

			got, err := runToString(t.Context(), "def main():\n    return "+tc.expression+".unwrap()\n", tools, allowAll, func(context.Context, string, string, map[string]any) (starlark.Value, error) {
				called = true
				return starlark.String(""), nil
			}, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Run() error = %v, want %q", err, tc.want)
			}

			if called || got != "" {
				t.Fatalf("Run() = %q, called = %t, want no result or tool call", got, called)
			}
		})
	}
}

func TestSanitizeAndStarlarkName(t *testing.T) {
	if got := SanitizeTool("create-issue"); got != "create_issue" {
		t.Fatalf("SanitizeTool(create-issue) = %q, want create_issue", got)
	}

	if got := StarlarkName("server", "create-issue"); got != "server_create_issue" {
		t.Fatalf("StarlarkName = %q, want server_create_issue", got)
	}

	if got := StarlarkName("sequential-thinking", "sequentialthinking"); got != "sequential_thinking_sequentialthinking" {
		t.Fatalf("StarlarkName(hyphen server) = %q", got)
	}
}

func TestBuildCatalogCollision(t *testing.T) {
	tools := []ToolDesc{
		{Server: "s", Name: "create-issue"},
		{Server: "s", Name: "create_issue"},
	}

	_, err := BuildCatalog(tools)
	if err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("BuildCatalog() error = %v, want collision", err)
	}

	got, err := runToString(t.Context(), "def main():\n    return 'unused'\n", tools, allowAll, nilCall, nil)
	if err == nil || !strings.Contains(err.Error(), "collision") || got != "" {
		t.Fatalf("Run() = %q, %v, want collision error and no result", got, err)
	}
}

func TestRunCancelledContextStops(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := runToString(ctx, `def main():
    while True:
        pass
    return "done"
`, nil, allowAll, nilCall, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want canceled")
	}
}

func TestToolIsNotCalledOnceItsScriptIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	thread := &starlark.Thread{}
	thread.SetLocal(localCtx, ctx)

	called := false
	tool := newKwargsValueBuiltin("probe", HostTool{Name: "probe", CallValue: func(context.Context, map[string]any) (starlark.Value, error) {
		called = true

		return starlark.None, nil
	}})

	if _, err := starlark.Call(thread, tool, nil, nil); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("call after cancel: err = %v, called = %v; want context.Canceled and no call", err, called)
	}
}

func TestRunCallsTool(t *testing.T) {
	tools := []ToolDesc{{
		Server: "demo",
		Name:   "echo",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"message": map[string]any{"type": "string"}},
			"required":   []any{"message"},
		},
	}}

	var (
		gotServer, gotName, gotSubject string
		gotArgs                        map[string]any
	)

	result, err := runToString(t.Context(), `def main():
    return demo_echo(message="hi")
`, tools, func(_ context.Context, subject string, args map[string]any) error {
		gotSubject = subject
		gotArgs = args

		return nil
	}, func(_ context.Context, server, name string, args map[string]any) (starlark.Value, error) {
		gotServer, gotName = server, name

		if args["message"] != "hi" {
			t.Fatalf("call args = %#v", args)
		}

		return starlark.String("echoed"), nil
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result != "echoed" || gotServer != "demo" || gotName != "echo" || gotSubject != "demo.echo" || gotArgs["message"] != "hi" {
		t.Fatalf("result=%q server=%q name=%q subject=%q args=%#v", result, gotServer, gotName, gotSubject, gotArgs)
	}
}

func nilCall(context.Context, string, string, map[string]any) (starlark.Value, error) {
	return nil, errors.New("unexpected call")
}

func TestRunHostTool(t *testing.T) {
	var keys []string

	got, err := runToString(t.Context(), `def main():
    first = read(filePath="a.txt")
    return first + read(filePath="a.txt")
`, nil, allowAll, nilCall, []HostTool{{
		Details: NoDetails{},
		Name:    "read",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"filePath": map[string]any{"type": "string"}},
			"required":   []any{"filePath"},
		},
		CallValue: func(ctx context.Context, args map[string]any) (starlark.Value, error) {
			if args["filePath"] != "a.txt" {
				t.Fatalf("args = %#v", args)
			}

			keys = append(keys, CallKey(ctx))

			return starlark.String("contents"), nil
		},
	}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got != "contentscontents" || strings.Join(keys, ",") != "0,1" {
		t.Fatalf("Run() = %q, call keys = %q, want contentscontents and [0 1]", got, keys)
	}
}

func TestRunRejectsHostToolNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		host []HostTool
		want string
	}{
		{"MCP collision", []HostTool{{Details: NoDetails{}, Name: "demo_echo", CallValue: nilCallHost}}, `builtin name collision "demo_echo"`},
		{"JSON collision", []HostTool{{Details: NoDetails{}, Name: "json", CallValue: nilCallHost}}, `builtin name collision "json"`},
		{"concurrency collision", []HostTool{{Details: NoDetails{}, Name: "gather", CallValue: nilCallHost}}, `builtin name collision "gather"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runToString(t.Context(), "def main():\n    return 'unused'\n", []ToolDesc{{Server: "demo", Name: "echo"}}, allowAll, nilCall, tc.host)
			if err == nil || err.Error() != tc.want || got != "" {
				t.Fatalf("Run() = %q, %v, want no result and %q", got, err, tc.want)
			}
		})
	}
}

func TestRunHostToolValueAndError(t *testing.T) {
	errHost := errors.New("host failed")

	for _, tc := range []struct {
		name string
		tool HostTool
		want string
	}{
		{
			name: "tuple value",
			tool: HostTool{
				Details: NoDetails{},
				Name:    "read",
				CallValue: func(context.Context, map[string]any) (starlark.Value, error) {
					return starlark.Tuple{starlark.String("contents"), starlark.MakeInt(7)}, nil
				},
			},
			want: `["contents",7]`,
		},
		{
			name: "call failure",
			tool: HostTool{
				Details: NoDetails{},
				Name:    "read",
				CallValue: func(context.Context, map[string]any) (starlark.Value, error) {
					return starlark.String("partial contents"), errHost
				},
			},
			want: "read: host failed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runToString(t.Context(), "def main():\n    return read()\n", nil, allowAll, nilCall, []HostTool{tc.tool})
			if got != tc.want || err != nil {
				t.Fatalf("Run() = %q, %v, want %q", got, err, tc.want)
			}
		})
	}
}

func nilCallHost(context.Context, map[string]any) (starlark.Value, error) {
	return nil, errors.New("unexpected host call")
}

func TestRunReturnsFinalValue(t *testing.T) {
	got, err := Run(t.Context(), "def main():\n    return [t(), 1]\n", nil, allowAll, nilCall, valueHosts(map[string]starlark.Value{"t": starlark.String("x")}))
	if err != nil {
		t.Fatal(err)
	}

	list, ok := got.(*starlark.List)
	if !ok || list.Len() != 2 || list.Index(0).Type() != "text" {
		t.Fatalf("Run() = %s %v, want a list holding a text", got.Type(), got)
	}

	if list.Append(starlark.None) == nil {
		t.Fatal("Run() returned a mutable value")
	}
}

func TestRunFileArguments(t *testing.T) {
	file := NewFile(FileSpec{Name: "chart.png", Mime: "image/png", Content: "png-bytes"})
	tool := func(name string, fileArgs ...string) HostTool {
		return HostTool{
			Details:  NoDetails{},
			Name:     name,
			FileArgs: fileArgs,
			CallValue: func(ctx context.Context, args map[string]any) (starlark.Value, error) {
				got, ok := ArgFile(ctx, file.SHA256())
				if !ok || got != file {
					return nil, errors.New("file not reachable")
				}

				encoded, _ := json.Marshal(args)

				return starlark.String(encoded), nil
			},
		}
	}

	descriptor := `{"file":"chart.png","mime":"image/png","sha256":"` + file.SHA256() + `","size":9,"source":"","summary":"chart.png, image/png, 9 B"}`
	refused := func(tool, arg string) string {
		return tool + ": argument " + arg + " got a file object; pass chart.base64(), a path, or text"
	}

	for _, tc := range []struct {
		expression, want string
	}{
		{`bash(command="cat > x", stdin=f())`, `{"command":"cat \u003e x","stdin":` + descriptor + `}`},
		{`decision(images=[f(), "a.png"])`, `{"images":[` + descriptor + `,"a.png"]}`},
		{`decision(images=try_gather([f]))`, `{"images":[` + descriptor + `]}`},
		{`decision(images=f()).error`, refused("decision", "images")},
		{`bash(command=f()).error`, refused("bash", "command")},
		{`bash(stdin={"x": f()}).error`, refused("bash", "stdin")},
		{`bash(stdin=[f()]).error`, refused("bash", "stdin")},
		{`bash(stdin={f(): 1}).error`, refused("bash", "stdin")},
		{`upload(image=(f(),)).error`, refused("upload", "image")},
		{`upload(image=try_gather([f])).error`, refused("upload", "image")},
	} {
		t.Run(tc.expression, func(t *testing.T) {
			hosts := append(valueHosts(map[string]starlark.Value{"f": file}), tool("bash", "stdin"), tool("decision", "images[]"), tool("upload"))

			got, err := runToString(t.Context(), "def main():\n    return "+tc.expression+"\n", nil, allowAll, nilCall, hosts)
			if err != nil || got != tc.want {
				t.Fatalf("runToString() = %q, %v, want %q", got, err, tc.want)
			}
		})
	}
}
