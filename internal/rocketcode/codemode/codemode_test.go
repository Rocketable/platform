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

func noToolCallObserver(context.Context, []string, string, map[string]any) {
}

func TestRunRawStringKeepsUnknownEscapes(t *testing.T) {
	got, err := Run(t.Context(), `def main():
    return r"find . \( -name '*.go' \)"
`, nil, allowAll, nilCall, noToolCallObserver, nil)
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

			_, err := Run(t.Context(), tc.source, nil, allowAll, nilCall, noToolCallObserver, []HostTool{{
				Name: "bash",
				Call: func(context.Context, map[string]any) (string, error) {
					called = true
					return "dispatched", nil
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

	out, err := Run(t.Context(), `def main():
    return bash(command=r'''python3 - <<'PY'
print("hello")
PY''')
`, nil, allowAll, nilCall, noToolCallObserver, []HostTool{{
		Name: "bash",
		Call: func(_ context.Context, args map[string]any) (string, error) {
			got, _ = args["command"].(string)
			return "ok", nil
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
	got, err := Run(t.Context(), `def main():
    return r'''rg -F -- "$pattern" file.txt'''
`, nil, allowAll, nilCall, noToolCallObserver, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got != `rg -F -- "$pattern" file.txt` {
		t.Fatalf("Run() = %q", got)
	}
}

func TestRunMainReturnsDictJSON(t *testing.T) {
	got, err := Run(t.Context(), `def main():
    return {"a": 1, "b": True}
`, nil, allowAll, nilCall, noToolCallObserver, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got != `{"a":1,"b":true}` {
		t.Fatalf("Run() = %q, want JSON object", got)
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
		{"unencodable result", "def main():\n    return lambda: 'unused'\n", "encode main result:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Run(t.Context(), tc.source, nil, allowAll, nilCall, noToolCallObserver, nil)
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

	_, err := Run(t.Context(), `def main():
    return demo_echo(message="hi")
`, tools, func(context.Context, string, map[string]any) error {
		return errors.New("denied")
	}, func(context.Context, string, string, map[string]any) (string, error) {
		called = true
		return "", nil
	}, noToolCallObserver, nil)
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
			want:       "demo_echo: invalid arguments: validate args:",
			schema: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"message": map[string]any{"type": "string"}},
				"required":             []any{"message"},
				"additionalProperties": false,
			},
		},
		{"positional argument", `demo_echo("hi")`, "demo_echo: only keyword arguments are allowed", nil},
		{"unencodable argument", `demo_echo(message=lambda: "hi")`, "demo_echo arguments: encode kwargs:", nil},
		{"invalid schema type", `demo_echo()`, "parse input schema:", map[string]any{"type": 1}},
		{"unresolved schema reference", `demo_echo()`, "resolve input schema:", map[string]any{"$ref": "#/$defs/missing"}},
		{"unencodable schema", `demo_echo()`, "encode input schema:", map[string]any{"default": make(chan string)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			tools := []ToolDesc{{Server: "demo", Name: "echo", InputSchema: tc.schema}}

			got, err := Run(t.Context(), "def main():\n    return "+tc.expression+"\n", tools, allowAll, func(context.Context, string, string, map[string]any) (string, error) {
				called = true
				return "", nil
			}, noToolCallObserver, nil)
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

	got, err := Run(t.Context(), "def main():\n    return 'unused'\n", tools, allowAll, nilCall, noToolCallObserver, nil)
	if err == nil || !strings.Contains(err.Error(), "collision") || got != "" {
		t.Fatalf("Run() = %q, %v, want collision error and no result", got, err)
	}
}

func TestRunCancelledContextStops(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := Run(ctx, `def main():
    while True:
        pass
    return "done"
`, nil, allowAll, nilCall, noToolCallObserver, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want canceled")
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

	result, err := Run(t.Context(), `def main():
    return demo_echo(message="hi")
`, tools, func(_ context.Context, subject string, args map[string]any) error {
		gotSubject = subject
		gotArgs = args

		return nil
	}, func(_ context.Context, server, name string, args map[string]any) (string, error) {
		gotServer, gotName = server, name

		if args["message"] != "hi" {
			t.Fatalf("call args = %#v", args)
		}

		return "echoed", nil
	}, noToolCallObserver, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result != "echoed" || gotServer != "demo" || gotName != "echo" || gotSubject != "demo.echo" || gotArgs["message"] != "hi" {
		t.Fatalf("result=%q server=%q name=%q subject=%q args=%#v", result, gotServer, gotName, gotSubject, gotArgs)
	}
}

func nilCall(context.Context, string, string, map[string]any) (string, error) {
	return "", errors.New("unexpected call")
}

func TestRunHostTool(t *testing.T) {
	var keys []string

	got, err := Run(t.Context(), `def main():
    first = read(filePath="a.txt")
    return first + read(filePath="a.txt")
`, nil, allowAll, nilCall, noToolCallObserver, []HostTool{{
		Name: "read",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"filePath": map[string]any{"type": "string"}},
			"required":   []any{"filePath"},
		},
		Call: func(ctx context.Context, args map[string]any) (string, error) {
			if args["filePath"] != "a.txt" {
				t.Fatalf("args = %#v", args)
			}

			keys = append(keys, CallKey(ctx))

			return "contents", nil
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
		{"empty name", []HostTool{{Call: nilCallHost}}, "host tool name is required"},
		{"MCP collision", []HostTool{{Name: "demo_echo", Call: nilCallHost}}, `builtin name collision "demo_echo"`},
		{"concurrency collision", []HostTool{{Name: "gather", Call: nilCallHost}}, `builtin name collision "gather"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Run(t.Context(), "def main():\n    return 'unused'\n", []ToolDesc{{Server: "demo", Name: "echo"}}, allowAll, nilCall, noToolCallObserver, tc.host)
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
		err  error
	}{
		{
			name: "value takes precedence over string call",
			tool: HostTool{
				Name: "read",
				Call: nilCallHost,
				CallValue: func(context.Context, map[string]any) (starlark.Value, error) {
					return starlark.Tuple{starlark.String("contents"), starlark.MakeInt(7)}, nil
				},
			},
			want: `["contents",7]`,
		},
		{
			name: "string call failure",
			tool: HostTool{
				Name: "read",
				Call: func(context.Context, map[string]any) (string, error) {
					return "partial contents", errHost
				},
			},
			err: errHost,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Run(t.Context(), "def main():\n    return read()\n", nil, allowAll, nilCall, noToolCallObserver, []HostTool{tc.tool})
			if got != tc.want || !errors.Is(err, tc.err) {
				t.Fatalf("Run() = %q, %v, want %q, %v", got, err, tc.want, tc.err)
			}
		})
	}
}

func nilCallHost(context.Context, map[string]any) (string, error) {
	return "", errors.New("unexpected host call")
}
