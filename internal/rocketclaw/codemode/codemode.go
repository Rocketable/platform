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
	CallValue   func(ctx context.Context, args map[string]any) (starlark.Value, error)
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

func Run(ctx context.Context, source string, tools []ToolDesc, decide DecideFunc, call CallFunc, host []HostTool) (starlark.Value, error) {
	catalog, errCatalog := BuildCatalog(tools)
	if errCatalog != nil {
		return nil, errCatalog
	}

	concurrency := concurrencyBuiltins()
	comparison := comparisonBuiltins()

	predeclared := make(starlark.StringDict, 1+len(catalog)+len(host)+len(concurrency)+len(comparison))

	predeclared["json"] = jsonModule()
	for name, tool := range catalog {
		predeclared[name] = newKwargsValueBuiltin(name, tool.InputSchema, nil, func(ctx context.Context, callArgs map[string]any) (starlark.Value, error) {
			if err := decide(ctx, tool.Server+"."+tool.Name, callArgs); err != nil {
				return nil, err
			}

			return call(ctx, tool.Server, tool.Name, callArgs)
		})
	}

	for _, tool := range host {
		if tool.Name == "" {
			return nil, errors.New("host tool name is required")
		}

		if _, exists := predeclared[tool.Name]; exists {
			return nil, fmt.Errorf("builtin name collision %q", tool.Name)
		}

		predeclared[tool.Name] = newKwargsValueBuiltin(tool.Name, tool.InputSchema, tool.FileArgs, tool.CallValue)
	}

	for name, builtin := range concurrency {
		if _, exists := predeclared[name]; exists {
			return nil, fmt.Errorf("builtin name collision %q", name)
		}

		predeclared[name] = builtin
	}

	maps.Copy(predeclared, comparison)

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

	thread, stopCancel := newCancellableThread(ctx, "codemode", stepBudget)
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

func jsonModule() *starlarkstruct.Module {
	members := maps.Clone(starjson.Module.Members)
	members["decode"] = starlark.NewBuiltin("json.decode", func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if len(args) > 0 {
			if text, ok := args[0].(Text); ok {
				args = append(starlark.Tuple{starlark.String(text.Text())}, args[1:]...)
			}
		}

		decoded, err := starlark.Call(thread, starjson.Module.Members["decode"], args, kwargs)
		if err != nil {
			return nil, fmt.Errorf("%w", err)
		}

		return decoded, nil
	})

	return &starlarkstruct.Module{Name: "json", Members: members}
}

type argFilesContext struct{}

func ArgFile(ctx context.Context, sha256 string) (*File, bool) {
	file, ok := ctx.Value(argFilesContext{}).(map[string]*File)[sha256]

	return file, ok
}

func collectArgFiles(v starlark.Value, position string, allowed []string, files map[string]*File) *File {
	switch v := v.(type) {
	case *File:
		if !slices.Contains(allowed, position) {
			return v
		}

		files[v.SHA256()] = v
	case *starlark.List, starlark.Tuple, *starlark.Set:
		iter := starlark.Iterate(v)
		defer iter.Done()

		var elem starlark.Value
		for iter.Next(&elem) {
			if file := collectArgFiles(elem, position+"[]", allowed, files); file != nil {
				return file
			}
		}
	case *starlark.Dict:
		for _, item := range v.Items() {
			for _, part := range item {
				if file := collectArgFiles(part, position+"{}", allowed, files); file != nil {
					return file
				}
			}
		}
	}

	return nil
}

func newKwargsValueBuiltin(name string, schema map[string]any, fileArgs []string, call func(context.Context, map[string]any) (starlark.Value, error)) *starlark.Builtin {
	return starlark.NewBuiltin(name, func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if len(args) != 0 {
			return nil, fmt.Errorf("%s: only keyword arguments are allowed", b.Name())
		}

		files := map[string]*File{}

		for _, kv := range kwargs {
			arg := string(kv[0].(starlark.String))
			if file := collectArgFiles(kv[1], arg, fileArgs, files); file != nil {
				stem := strings.TrimSuffix(file.Name(), path.Ext(file.Name()))

				return NewError(b.Name(), fmt.Sprintf("argument %q got a file object (%s). Pass a path, text, or %s.base64().", arg, file.Name(), stem)), nil
			}
		}

		callArgs, errArgs := kwargsToMap(thread, kwargs)
		if errArgs != nil {
			return NewError(b.Name(), "invalid arguments: "+errArgs.Error()), nil
		}

		if err := validateArgs(schema, callArgs); err != nil {
			return NewError(b.Name(), "invalid arguments: "+err.Error()), nil
		}

		ctx := thread.Local(localCtx).(context.Context)
		callCtx := context.WithValue(context.WithValue(ctx, callKeyContext{}, nextKey(thread)), argFilesContext{}, files)

		value, err := call(callCtx, callArgs)
		if err != nil && context.Cause(ctx) != nil {
			return nil, err
		}

		return HostResult(b.Name(), value, err), nil
	})
}

func kwargsToMap(thread *starlark.Thread, kwargs []starlark.Tuple) (map[string]any, error) {
	if len(kwargs) == 0 {
		return map[string]any{}, nil
	}

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

func validateArgs(schemaMap, args map[string]any) error {
	if schemaMap == nil {
		return nil
	}

	raw, err := json.Marshal(schemaMap)
	if err != nil {
		return fmt.Errorf("encode input schema: %w", err)
	}

	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return fmt.Errorf("parse input schema: %w", err)
	}

	resolved, err := schema.Resolve(nil)
	if err != nil {
		return fmt.Errorf("resolve input schema: %w", err)
	}

	if err := resolved.Validate(args); err != nil {
		return fmt.Errorf("validate args: %w", err)
	}

	return nil
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
