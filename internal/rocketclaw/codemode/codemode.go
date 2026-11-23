package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"sync"
	"unicode"

	"github.com/google/jsonschema-go/jsonschema"
	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
	"go.starlark.net/syntax"
)

const stepBudget = 1_000_000

func sourceOptions() *syntax.FileOptions {
	return &syntax.FileOptions{
		While:           true,
		Set:             true,
		TopLevelControl: false,
		GlobalReassign:  false,
		Recursion:       false,
	}
}

type ToolDesc struct {
	Server, Name, Description string
	InputSchema               map[string]any
}

type CallFunc func(ctx context.Context, server, name string, args map[string]any) (starlark.Value, error)

type DecideFunc func(ctx context.Context, subject string, args map[string]any) error

type HostTool struct {
	Name        string
	InputSchema map[string]any
	FileArgs    []string
	Details     CallDetails
	CallValue   func(ctx context.Context, args map[string]any) (starlark.Value, error)
}

type CallDetails interface {
	Details(args map[string]any) starlark.StringDict
}

type NoDetails struct{}

func (NoDetails) Details(map[string]any) starlark.StringDict { return nil }

type mcpCallDetails ToolDesc

func (d mcpCallDetails) Details(map[string]any) starlark.StringDict {
	return starlark.StringDict{"server": starlark.String(d.Server), "tool": starlark.String(d.Name)}
}

func StarlarkName(server, mcpToolName string) string {
	return SanitizeTool(server) + "_" + SanitizeTool(mcpToolName)
}

func SanitizeTool(mcpToolName string) string {
	var b strings.Builder
	b.Grow(len(mcpToolName))

	for _, r := range mcpToolName {
		if r <= unicode.MaxASCII && (r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteByte(byte(r))
			continue
		}

		b.WriteByte('_')
	}

	s := b.String()
	if s == "" || s[0] >= '0' && s[0] <= '9' {
		return "_" + s
	}

	return s
}

func BuildCatalog(tools []ToolDesc) (map[string]ToolDesc, error) {
	out := make(map[string]ToolDesc, len(tools))
	for _, tool := range tools {
		name := StarlarkName(tool.Server, tool.Name)
		if prev, exists := out[name]; exists {
			return nil, fmt.Errorf("starlark name collision %q between %s.%s and %s.%s", name, prev.Server, prev.Name, tool.Server, tool.Name)
		}

		out[name] = tool
	}

	return out, nil
}

func FreeNames(source string) ([]string, error) {
	var names []string

	_, _, err := starlark.SourceProgramOptions(sourceOptions(), "codemode.star", source, func(name string) bool {
		names = append(names, name)

		return true
	})
	if err != nil {
		return nil, wrapParseError(err)
	}

	return names, nil
}

func Run(ctx context.Context, source string, tools []ToolDesc, decide DecideFunc, call CallFunc, host []HostTool) (starlark.Value, error) {
	catalog, errCatalog := BuildCatalog(tools)
	if errCatalog != nil {
		return nil, errCatalog
	}

	concurrency := concurrencyBuiltins()
	members := maps.Clone(starjson.Module.Members)
	members["decode"] = starlark.NewBuiltin("json.decode", decodeJSON)
	predeclared := starlark.StringDict{"json": &starlarkstruct.Module{Name: "json", Members: members}}

	for name, tool := range catalog {
		predeclared[name] = newKwargsValueBuiltin(name, HostTool{
			Name:        tool.Server + "." + tool.Name,
			InputSchema: tool.InputSchema,
			Details:     mcpCallDetails(tool),
			CallValue: func(ctx context.Context, callArgs map[string]any) (starlark.Value, error) {
				if err := decide(ctx, tool.Server+"."+tool.Name, callArgs); err != nil {
					return nil, err
				}

				return call(ctx, tool.Server, tool.Name, callArgs)
			},
		})
	}

	for _, tool := range host {
		if _, exists := predeclared[tool.Name]; exists {
			return nil, fmt.Errorf("builtin name collision %q", tool.Name)
		}

		predeclared[tool.Name] = newKwargsValueBuiltin(tool.Name, tool)
	}

	for name, builtin := range concurrency {
		if _, exists := predeclared[name]; exists {
			return nil, fmt.Errorf("builtin name collision %q", name)
		}

		predeclared[name] = builtin
	}

	maps.Copy(predeclared, comparisonBuiltins())

	if err := context.Cause(ctx); err != nil {
		return nil, fmt.Errorf("codemode context: %w", err)
	}

	f, errParse := sourceOptions().Parse("codemode.star", source, 0)
	if errParse != nil {
		return nil, wrapParseError(errParse)
	}

	rewriteComparisons(f)

	prog, errCompile := starlark.FileProgram(f, predeclared.Has)
	if errCompile != nil {
		return nil, wrapParseError(errCompile)
	}

	thread, stopCancel := newCancellableThread(context.WithValue(ctx, sourceContext{}, source), "codemode", stepBudget)
	defer stopCancel()

	globals, errExec := prog.Init(thread, predeclared)
	globals.Freeze()

	if errExec != nil {
		if errCtx := context.Cause(ctx); errCtx != nil {
			return nil, fmt.Errorf("codemode context: %w", errCtx)
		}

		return nil, fmt.Errorf("execute codemode: %w", errExec)
	}

	main := globals["main"]
	if main == nil {
		return nil, errors.New("main is required")
	}

	fn, ok := main.(starlark.Callable)
	if !ok {
		return nil, errors.New("main must be a function")
	}

	value, errCall := starlark.Call(thread, fn, nil, nil)
	if errCall != nil {
		if errCtx := context.Cause(ctx); errCtx != nil {
			return nil, fmt.Errorf("codemode context: %w", errCtx)
		}

		return nil, fmt.Errorf("call main: %w", errCall)
	}

	if value == starlark.None {
		return nil, errors.New("main returned None")
	}

	value.Freeze()

	return value, nil
}

type argFilesContext struct{}

func ArgFile(ctx context.Context, sha256 string) (*File, bool) {
	file, ok := ctx.Value(argFilesContext{}).(map[string]*File)[sha256]

	return file, ok
}

type argumentWalk struct {
	allowed         []string
	files           map[string]*File
	arg, refusedArg string
	refused         *File
}

func (w *argumentWalk) failure(v starlark.Value, position string) starlark.Value {
	if IsFailure(v) {
		return v
	}

	switch v := unbox(v).(type) {
	case *File:
		switch {
		case slices.Contains(w.allowed, position):
			w.files[v.SHA256()] = v
		case w.refused == nil:
			w.refused, w.refusedArg = v, w.arg
		}
	case *starlark.List, starlark.Tuple, *starlark.Set:
		for elem := range starlark.Elements(v.(starlark.Iterable)) {
			if failure := w.failure(elem, position+"[]"); failure != nil {
				return failure
			}
		}
	case *starlark.Dict:
		for _, pair := range v.Items() {
			for _, part := range pair {
				if failure := w.failure(part, position+"{}"); failure != nil {
					return failure
				}
			}
		}
	}

	return nil
}

func newKwargsValueBuiltin(callable string, tool HostTool) *starlark.Builtin {
	resolved := sync.OnceValues(func() (*jsonschema.Resolved, error) { return resolveSchema(tool.InputSchema) })

	return starlark.NewBuiltin(tool.Name, func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		walk := argumentWalk{allowed: tool.FileArgs, files: map[string]*File{}}

		for _, arg := range args {
			if failure := walk.failure(arg, ""); failure != nil {
				return failure, nil
			}
		}

		for _, kv := range kwargs {
			walk.arg = string(kv[0].(starlark.String))
			if failure := walk.failure(kv[1], walk.arg); failure != nil {
				return failure, nil
			}
		}

		callArgs, errArgs := kwargsToMap(thread, kwargs)
		fail := func(message string) starlark.Value {
			return NewFailure(b.Name(), message, tool.Details.Details(callArgs))
		}

		switch {
		case len(args) != 0:
			var schema jsonschema.Schema
			if raw, err := json.Marshal(tool.InputSchema); err == nil {
				_ = json.Unmarshal(raw, &schema)
			}

			if len(schema.Required) == 1 {
				return fail(fmt.Sprintf("only keyword arguments are allowed; call %s(%s=...)", callable, schema.Required[0])), nil
			}

			return fail("only keyword arguments are allowed; use keyword arguments"), nil
		case walk.refused != nil:
			stem := strings.TrimSuffix(walk.refused.Name(), path.Ext(walk.refused.Name()))

			return fail(fmt.Sprintf("argument %s got a file object; pass %s.base64(), a path, or text", walk.refusedArg, stem)), nil
		case errArgs != nil:
			return fail("invalid arguments: " + errArgs.Error()), nil
		}

		inputSchema, errSchema := resolved()
		if errSchema != nil {
			return fail("invalid arguments: " + errSchema.Error()), nil
		}

		if inputSchema != nil {
			if err := inputSchema.Validate(callArgs); err != nil {
				return fail("invalid arguments: validate args: " + err.Error()), nil
			}
		}

		ctx := thread.Local(localCtx).(context.Context)
		if err := context.Cause(ctx); err != nil {
			return nil, fmt.Errorf("%s: %w", b.Name(), err)
		}

		callCtx := context.WithValue(context.WithValue(ctx, callKeyContext{}, nextKey(thread)), argFilesContext{}, walk.files)
		if thread.CallStackDepth() > 1 {
			callCtx = context.WithValue(callCtx, callLineContext{}, int(thread.CallFrame(1).Pos.Line))
		}

		value, err := tool.CallValue(callCtx, callArgs)
		if err != nil && context.Cause(ctx) != nil {
			return nil, err
		}

		result := HostResult(b.Name(), value, err)
		if failure, ok := result.(*Error); ok {
			return failure.withDetails(tool.Details.Details(callArgs)), nil
		}

		return result, nil
	})
}

func kwargsToMap(thread *starlark.Thread, kwargs []starlark.Tuple) (map[string]any, error) {
	dict := starlark.NewDict(len(kwargs))
	for _, kv := range kwargs {
		if err := dict.SetKey(kv[0], kv[1]); err != nil {
			return nil, fmt.Errorf("set kwarg: %w", err)
		}
	}

	encoded, err := starlark.Call(thread, starjson.Module.Members["encode"], starlark.Tuple{dict}, nil)
	if err != nil {
		return nil, fmt.Errorf("encode kwargs: %w", err)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(encoded.(starlark.String)), &out); err != nil {
		return nil, fmt.Errorf("decode kwargs: %w", err)
	}

	return out, nil
}

func resolveSchema(schemaMap map[string]any) (*jsonschema.Resolved, error) {
	if schemaMap == nil {
		return nil, nil
	}

	raw, err := json.Marshal(schemaMap)
	if err != nil {
		return nil, fmt.Errorf("encode input schema: %w", err)
	}

	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("parse input schema: %w", err)
	}

	resolved, err := schema.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("resolve input schema: %w", err)
	}

	return resolved, nil
}

type parseCategory string

const (
	parseCategoryUnexpectedNewline   parseCategory = "unexpected_newline"
	parseCategoryUnterminatedString  parseCategory = "unterminated_string"
	parseCategoryInvalidEscape       parseCategory = "invalid_escape"
	parseCategoryUnexpectedCharacter parseCategory = "unexpected_character"
	parseCategorySyntax              parseCategory = "syntax"
)

type parseDiagnostic struct {
	Phase            string `json:"phase"`
	Category         string `json:"category"`
	Message          string `json:"message"`
	Line             int    `json:"line"`
	Column           int    `json:"column"`
	ExecutionStarted bool   `json:"execution_started"`
	LikelyCause      string `json:"likely_cause"`
	RecommendedFix   string `json:"recommended_fix"`
}

type parseError struct {
	category       parseCategory
	likelyCause    string
	recommendedFix string
	err            syntax.Error
}

func wrapParseError(err error) error {
	syn, ok := errors.AsType[syntax.Error](err)
	if !ok {
		return fmt.Errorf("execute codemode: %w", err)
	}

	out := parseError{
		category:       parseCategorySyntax,
		likelyCause:    "Starlark rejected the wrapper source",
		recommendedFix: "Fix the Starlark syntax, then rerun. Failed wrapper output is not evidence.",
		err:            syn,
	}

	classifiers := []struct {
		needle   string
		category parseCategory
		cause    string
		fix      string
	}{
		{"unexpected newline", parseCategoryUnexpectedNewline, "A single-line string contains a literal newline", "Use a raw triple-quoted string (r'''...''') or a workspace-local helper"},
		{"unexpected EOF in string", parseCategoryUnterminatedString, "A string was not closed before the end of the program", "Close the string, or use r'''...''' for multiline source"},
		{"invalid escape", parseCategoryInvalidEscape, `Ordinary "..." treats backslash as a Starlark escape`, `Use r'''...'''`},
		{"unexpected input character", parseCategoryUnexpectedCharacter, "A quote closed too early, so a character such as $ was seen as Starlark", "Keep the character inside a correctly delimited string (r'''...''' for multiline)"},
		{"want ','", parseCategorySyntax, "Missing comma or a quote closed too early", "Fix the Starlark syntax around that token, then rerun"},
	}

	for _, classifier := range classifiers {
		if strings.Contains(syn.Msg, classifier.needle) {
			out.category = classifier.category
			out.likelyCause = classifier.cause
			out.recommendedFix = classifier.fix

			break
		}
	}

	return &out
}

func (e *parseError) Unwrap() error { return e.err }

func (e *parseError) Error() string {
	raw, _ := json.Marshal(parseDiagnostic{
		Phase:            "codemode_parse",
		Category:         string(e.category),
		Message:          e.err.Msg,
		Line:             int(e.err.Pos.Line),
		Column:           int(e.err.Pos.Col),
		ExecutionStarted: false,
		LikelyCause:      e.likelyCause,
		RecommendedFix:   e.recommendedFix,
	})

	return string(raw)
}
