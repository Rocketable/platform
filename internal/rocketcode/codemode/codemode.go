// Package codemode runs Starlark scripts that call outbound MCP tools.
package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/google/jsonschema-go/jsonschema"
	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

const stepBudget = 1_000_000

// ToolDesc is one MCP tool available inside a script / definitions listing.
type ToolDesc struct {
	Server, Name, Description string
	InputSchema               map[string]any
}

// CallFunc invokes MCP CallTool. server and name are raw MCP names.
type CallFunc func(ctx context.Context, server, name string, args map[string]any) (string, error)

// DecideFunc is per-call permission like bash. subject is "server.tool" (raw MCP tool name).
// Return nil to allow; error to deny/fail the Starlark builtin.
type DecideFunc func(ctx context.Context, subject string, args map[string]any) error

// ToolCallObserver observes a tool call and its Code Mode concurrency path.
type ToolCallObserver func(ctx context.Context, path []string, name string, args map[string]any)

// HostTool is a non-MCP builtin (e.g. read, glob). Call includes permission enforcement.
type HostTool struct {
	Name        string
	InputSchema map[string]any
	// Call returns a string result. Used when CallValue is nil.
	Call func(ctx context.Context, args map[string]any) (string, error)
	// CallValue returns a Starlark value. When set, it is preferred over Call.
	CallValue func(ctx context.Context, args map[string]any) (starlark.Value, error)
}

// StarlarkName returns sanitizedserver_sanitizedtool for a tool.
// Server names may contain '-' and '.' (MCP name charset); Starlark needs [A-Za-z0-9_].
func StarlarkName(server, mcpToolName string) string {
	return SanitizeTool(server) + "_" + SanitizeTool(mcpToolName)
}

// SanitizeTool maps non [A-Za-z0-9_] to _; prefixes _ if needed for valid ident start.
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

// BuildCatalog maps starlark name -> ToolDesc; fails on starlark name collision.
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

// Run executes Starlark source requiring def main() returning a value.
// mcp tools use decide+call; host tools use HostTool.Call (caller enforces permissions).
// observe receives each validated tool call before permission and execution.
// Returns string result (string as-is; other values JSON; None is error).
func Run(ctx context.Context, source string, tools []ToolDesc, decide DecideFunc, call CallFunc, observe ToolCallObserver, host []HostTool) (string, error) {
	catalog, errCatalog := BuildCatalog(tools)
	if errCatalog != nil {
		return "", errCatalog
	}

	concurrency := concurrencyBuiltins()

	predeclared := make(starlark.StringDict, len(catalog)+len(host)+len(concurrency))
	for name, tool := range catalog {
		if _, exists := predeclared[name]; exists {
			return "", fmt.Errorf("builtin name collision %q", name)
		}

		predeclared[name] = newMCPToolBuiltin(tool, decide, call, observe)
	}

	for _, tool := range host {
		if tool.Name == "" {
			return "", errors.New("host tool name is required")
		}

		if _, exists := predeclared[tool.Name]; exists {
			return "", fmt.Errorf("builtin name collision %q", tool.Name)
		}

		predeclared[tool.Name] = newHostToolBuiltin(tool, observe)
	}

	for name, builtin := range concurrency {
		if _, exists := predeclared[name]; exists {
			return "", fmt.Errorf("builtin name collision %q", name)
		}

		predeclared[name] = builtin
	}

	options := &syntax.FileOptions{
		While:           true,
		Set:             true,
		TopLevelControl: false,
		GlobalReassign:  false,
		Recursion:       false,
	}

	if err := context.Cause(ctx); err != nil {
		return "", fmt.Errorf("codemode context: %w", err)
	}

	thread, stopCancel := newCancellableThread(ctx, "codemode", stepBudget)
	defer stopCancel()

	globals, errExec := starlark.ExecFileOptions(options, thread, "codemode.star", source, predeclared)
	if errExec != nil {
		if errCtx := context.Cause(ctx); errCtx != nil {
			return "", fmt.Errorf("codemode context: %w", errCtx)
		}

		return "", fmt.Errorf("execute codemode: %w", errExec)
	}

	main := globals["main"]
	if main == nil {
		return "", errors.New("main is required")
	}

	fn, ok := main.(starlark.Callable)
	if !ok {
		return "", errors.New("main must be a function")
	}

	value, errCall := starlark.Call(thread, fn, nil, nil)
	if errCall != nil {
		if errCtx := context.Cause(ctx); errCtx != nil {
			return "", fmt.Errorf("codemode context: %w", errCtx)
		}

		return "", fmt.Errorf("call main: %w", errCall)
	}

	if value == starlark.None {
		return "", errors.New("main returned None")
	}

	if text, ok := starlark.AsString(value); ok {
		return text, nil
	}

	if value.Type() == "bash_result" {
		return value.String(), nil
	}

	encoded, errEncode := starlark.Call(thread, starjson.Module.Members["encode"], starlark.Tuple{value}, nil)
	if errEncode != nil {
		return "", fmt.Errorf("encode main result: %w", errEncode)
	}

	return string(encoded.(starlark.String)), nil
}

func newMCPToolBuiltin(tool ToolDesc, decide DecideFunc, call CallFunc, observe ToolCallObserver) *starlark.Builtin {
	return newKwargsBuiltin(StarlarkName(tool.Server, tool.Name), tool.InputSchema, observe, func(ctx context.Context, callArgs map[string]any) (string, error) {
		if err := decide(ctx, tool.Server+"."+tool.Name, callArgs); err != nil {
			return "", err
		}

		return call(ctx, tool.Server, tool.Name, callArgs)
	})
}

func newHostToolBuiltin(tool HostTool, observe ToolCallObserver) *starlark.Builtin {
	return newKwargsValueBuiltin(tool.Name, tool.InputSchema, observe, func(ctx context.Context, args map[string]any) (starlark.Value, error) {
		if tool.CallValue != nil {
			return tool.CallValue(ctx, args)
		}

		if tool.Call == nil {
			return nil, fmt.Errorf("%s: call is not configured", tool.Name)
		}

		result, err := tool.Call(ctx, args)
		if err != nil {
			return nil, err
		}

		return starlark.String(result), nil
	})
}

func newKwargsBuiltin(name string, schema map[string]any, observe ToolCallObserver, call func(context.Context, map[string]any) (string, error)) *starlark.Builtin {
	return newKwargsValueBuiltin(name, schema, observe, func(ctx context.Context, args map[string]any) (starlark.Value, error) {
		result, err := call(ctx, args)
		if err != nil {
			return nil, err
		}

		return starlark.String(result), nil
	})
}

func newKwargsValueBuiltin(name string, schema map[string]any, observe ToolCallObserver, call func(context.Context, map[string]any) (starlark.Value, error)) *starlark.Builtin {
	return starlark.NewBuiltin(name, func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if len(args) != 0 {
			return nil, fmt.Errorf("%s: only keyword arguments are allowed", b.Name())
		}

		callArgs, errArgs := kwargsToMap(thread, kwargs)
		if errArgs != nil {
			return nil, fmt.Errorf("%s arguments: %w", b.Name(), errArgs)
		}

		if err := validateArgs(schema, callArgs); err != nil {
			return nil, fmt.Errorf("%s: invalid arguments: %w", b.Name(), err)
		}

		ctx, errCtx := threadContext(thread)
		if errCtx != nil {
			return nil, errCtx
		}

		observe(ctx, threadPath(thread), name, callArgs)

		return call(ctx, callArgs)
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

	if out == nil {
		out = map[string]any{}
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

	instance := any(args)
	if args == nil {
		instance = map[string]any{}
	}

	if err := resolved.Validate(instance); err != nil {
		return fmt.Errorf("validate args: %w", err)
	}

	return nil
}
