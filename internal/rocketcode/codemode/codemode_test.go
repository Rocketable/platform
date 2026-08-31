package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func allowAll(context.Context, string, map[string]any) error { return nil }

func noToolCallObserver(context.Context, []string, string, map[string]any) {
}

func TestRunMainReturnsString(t *testing.T) {
	got, err := Run(t.Context(), `def main():
    return "hello"
`, nil, allowAll, nilCall, noToolCallObserver, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got != "hello" {
		t.Fatalf("Run() = %q, want hello", got)
	}
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
			source:   "def main():\n    return shell(command=r\"python3 - <<'PY'\nprint(\"hello\")\nPY\")\n",
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
			source:   "def main():\n    return shell(command=\"a\" command=\"b\")\n",
			category: parseCategorySyntax,
			message:  "want ','",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false

			_, err := Run(t.Context(), tc.source, nil, allowAll, nilCall, noToolCallObserver, []HostTool{{
				Name: "shell",
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
    return shell(command=r'''python3 - <<'PY'
print("hello")
PY''')
`, nil, allowAll, nilCall, noToolCallObserver, []HostTool{{
		Name: "shell",
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

func TestRunMainReturnsNoneError(t *testing.T) {
	_, err := Run(t.Context(), `def main():
    return None
`, nil, allowAll, nilCall, noToolCallObserver, nil)
	if err == nil || !strings.Contains(err.Error(), "None") {
		t.Fatalf("Run() error = %v, want None error", err)
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
	called := false
	tools := []ToolDesc{{
		Server: "demo",
		Name:   "echo",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"message": map[string]any{"type": "string"}},
			"required":             []any{"message"},
			"additionalProperties": false,
		},
	}}

	_, err := Run(t.Context(), `def main():
    return demo_echo(other="x")
`, tools, allowAll, func(context.Context, string, string, map[string]any) (string, error) {
		called = true
		return "", nil
	}, noToolCallObserver, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want schema error")
	}

	if called {
		t.Fatal("call invoked with invalid args")
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
	_, err := BuildCatalog([]ToolDesc{
		{Server: "s", Name: "create-issue"},
		{Server: "s", Name: "create_issue"},
	})
	if err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("BuildCatalog() error = %v, want collision", err)
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
	got, err := Run(t.Context(), `def main():
    return read(filePath="a.txt")
`, nil, allowAll, nilCall, noToolCallObserver, []HostTool{{
		Name: "read",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"filePath": map[string]any{"type": "string"}},
			"required":   []any{"filePath"},
		},
		Call: func(_ context.Context, args map[string]any) (string, error) {
			if args["filePath"] != "a.txt" {
				t.Fatalf("args = %#v", args)
			}

			return "contents", nil
		},
	}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got != "contents" {
		t.Fatalf("Run() = %q, want contents", got)
	}
}
