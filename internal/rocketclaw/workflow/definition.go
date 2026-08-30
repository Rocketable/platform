// Package workflow loads and validates saved RocketClaw workflows.
package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"

	"github.com/google/jsonschema-go/jsonschema"
	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

const validationStepLimit = 10_000

var workflowNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Definition is a compiled workflow definition.
type Definition struct {
	Name         string
	Description  string
	Phases       []string
	WorkerModels []string
	program      *starlark.Program
}

// Descriptions returns sorted name/description pairs for loaded definitions.
func Descriptions(definitions map[string]*Definition) []protocol.WorkflowDescription {
	out := make([]protocol.WorkflowDescription, 0, len(definitions))
	for _, name := range slices.Sorted(maps.Keys(definitions)) {
		out = append(out, protocol.WorkflowDescription{Name: name, Description: definitions[name].Description})
	}

	return out
}

// Load loads top-level workflow definitions from runtimeDir.
func Load(root *os.Root, runtimeDir string) (definitions map[string]*Definition, err error) {
	directory := path.Join(runtimeDir, "workflows")

	workflowsRoot, errOpenRoot := root.OpenRoot(directory)
	if errors.Is(errOpenRoot, fs.ErrNotExist) {
		return map[string]*Definition{}, nil
	}

	if errOpenRoot != nil {
		return nil, fmt.Errorf("open workflows directory: %w", errOpenRoot)
	}

	defer func() {
		if errClose := workflowsRoot.Close(); errClose != nil {
			err = errors.Join(err, fmt.Errorf("close workflows directory: %w", errClose))
		}
	}()

	entries, errReadDir := fs.ReadDir(workflowsRoot.FS(), ".")
	if errReadDir != nil {
		return nil, fmt.Errorf("read workflows directory: %w", errReadDir)
	}

	definitions = make(map[string]*Definition)

	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".star" {
			continue
		}

		name := entry.Name()[:len(entry.Name())-len(".star")]
		if len(name) > 64 || !workflowNamePattern.MatchString(name) {
			return nil, fmt.Errorf("workflow name %q must be lowercase hyphenated and at most 64 characters", name)
		}

		filename := path.Join(directory, entry.Name())

		file, errOpen := workflowsRoot.Open(entry.Name())
		if errOpen != nil {
			return nil, fmt.Errorf("open workflow %s: %w", entry.Name(), errOpen)
		}

		info, errInfo := file.Stat()
		if errInfo != nil {
			errClose := file.Close()
			if errClose != nil {
				errClose = fmt.Errorf("close workflow %s: %w", entry.Name(), errClose)
			}

			return nil, errors.Join(fmt.Errorf("inspect workflow %s: %w", entry.Name(), errInfo), errClose)
		}

		if !info.Mode().IsRegular() {
			if errClose := file.Close(); errClose != nil {
				return nil, fmt.Errorf("close workflow %s: %w", entry.Name(), errClose)
			}

			continue
		}

		source, errRead := io.ReadAll(file)

		errClose := file.Close()
		if errRead != nil {
			if errClose != nil {
				errClose = fmt.Errorf("close workflow %s: %w", entry.Name(), errClose)
			}

			return nil, errors.Join(fmt.Errorf("read workflow %s: %w", entry.Name(), errRead), errClose)
		}

		if errClose != nil {
			return nil, fmt.Errorf("close workflow %s: %w", entry.Name(), errClose)
		}

		definition, errCompile := compileDefinition(filename, name, source)
		if errCompile != nil {
			return nil, fmt.Errorf("validate workflow %s: %w", entry.Name(), errCompile)
		}

		definitions[name] = definition
	}

	return definitions, nil
}

func compileDefinition(filename, filenameStem string, source []byte) (*Definition, error) {
	predeclared := starlark.StringDict{
		"worker": starlark.NewBuiltin("worker", func(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
			return starlark.None, nil
		}),
	}
	for _, name := range []string{"agent", "parallel", "pipeline", "phase"} {
		predeclared[name] = starlark.NewBuiltin(name, func(_ *starlark.Thread, fn *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
			return nil, fmt.Errorf("%s is unavailable during workflow validation", fn.Name())
		})
	}

	options := &syntax.FileOptions{
		While:           true,
		Set:             true,
		TopLevelControl: false,
		GlobalReassign:  false,
		Recursion:       false,
	}

	file, errParse := options.Parse(filename, source, 0)
	if errParse != nil {
		return nil, fmt.Errorf("validate top-level syntax: %w", errParse)
	}

	name, description, phases, workerModels, errValidate := validateFile(options, file, filenameStem)
	if errValidate != nil {
		return nil, errValidate
	}

	program, errProgram := starlark.FileProgram(file, predeclared.Has)
	if errProgram != nil {
		return nil, fmt.Errorf("compile workflow: %w", errProgram)
	}

	thread := &starlark.Thread{Name: "workflow validation"}
	thread.SetMaxExecutionSteps(validationStepLimit)

	globals, errInit := program.Init(thread, predeclared)
	if errInit != nil {
		return nil, fmt.Errorf("initialize workflow: %w", errInit)
	}

	main, ok := globals["main"].(*starlark.Function)
	if !ok {
		return nil, errors.New("main must be a function")
	}

	if main.NumParams() != 1 || main.NumKwonlyParams() != 0 || main.HasVarargs() || main.HasKwargs() || main.ParamDefault(0) != nil {
		return nil, errors.New("main must have exactly one ordinary required parameter")
	}

	return &Definition{
		Name:         name,
		Description:  description,
		Phases:       phases,
		WorkerModels: workerModels,
		program:      program,
	}, nil
}

func validateFile(options *syntax.FileOptions, file *syntax.File, filenameStem string) (name, description string, phases, workerModels []string, err error) {
	functions := make(map[string]*syntax.DefStmt)
	literals := make(map[string]syntax.Expr)

	var meta *syntax.DictExpr

	for _, statement := range file.Stmts {
		switch statement := statement.(type) {
		case *syntax.DefStmt:
			functions[statement.Name.Name] = statement
			if errBody := validateFunctionBody(statement); errBody != nil {
				return "", "", nil, nil, errBody
			}
		case *syntax.AssignStmt:
			identifier, ok := statement.LHS.(*syntax.Ident)
			if !ok || statement.Op != syntax.EQ {
				return "", "", nil, nil, errors.New("top-level assignments must bind one name")
			}

			if identifier.Name == "meta" {
				var ok bool

				meta, ok = statement.RHS.(*syntax.DictExpr)
				if !ok || !isJSONLiteral(statement.RHS) {
					return "", "", nil, nil, errors.New("meta must be a literal dict")
				}

				continue
			}

			if call, ok := statement.RHS.(*syntax.CallExpr); ok {
				model, errWorker := validateWorker(call)
				if errWorker != nil {
					return "", "", nil, nil, errWorker
				}

				if model != "" {
					workerModels = append(workerModels, model)
				}

				continue
			}

			if !isJSONLiteral(statement.RHS) {
				return "", "", nil, nil, fmt.Errorf("global %s must be a JSON-compatible literal", identifier.Name)
			}

			literals[identifier.Name] = statement.RHS
		case *syntax.LoadStmt:
			return "", "", nil, nil, errors.New("load statements are not allowed")
		default:
			return "", "", nil, nil, errors.New("top-level statements must be function definitions or assignments")
		}
	}

	if meta == nil {
		return "", "", nil, nil, errors.New("meta must be a literal dict")
	}

	if errSchema := validateAgentSchemas(options, file, literals); errSchema != nil {
		return "", "", nil, nil, errSchema
	}

	if errFanout := validateNestedFanout(file, functions); errFanout != nil {
		return "", "", nil, nil, errFanout
	}

	name, description, phases, err = parseMeta(meta, filenameStem)

	return name, description, phases, workerModels, err
}

func validateFunctionBody(statement *syntax.DefStmt) error {
	for _, parameter := range statement.Params {
		if defaultValue, ok := parameter.(*syntax.BinaryExpr); ok && isEffectfulDefault(defaultValue.Y) {
			return fmt.Errorf("function %s has an effectful default", statement.Name.Name)
		}
	}

	var (
		errBody error
		inspect func(syntax.Node) bool
	)

	inspect = func(node syntax.Node) bool {
		if errBody != nil || node == nil {
			return false
		}

		if named, ok := node.(*syntax.BinaryExpr); ok && named.Op == syntax.EQ {
			syntax.Walk(named.Y, inspect)
			return false
		}

		if identifier, ok := node.(*syntax.Ident); ok && identifier.Name == "worker" {
			errBody = errors.New("worker may not be referenced inside a workflow function")
			return false
		}

		call, ok := node.(*syntax.CallExpr)
		if !ok {
			return true
		}

		if name, ok := callName(call); ok && (name == "print" || name == "worker") {
			errBody = fmt.Errorf("%s may not be called inside a workflow function", name)
			return false
		}

		return true
	}
	syntax.Walk(statement, inspect)

	return errBody
}

func validateAgentSchemas(options *syntax.FileOptions, file *syntax.File, literals map[string]syntax.Expr) error {
	var errSchema error

	validated := make(map[syntax.Expr]bool)

	syntax.Walk(file, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || errSchema != nil {
			return errSchema == nil
		}

		if name, ok := callName(call); !ok || name != "agent" {
			return true
		}

		var schemaExpression syntax.Expr
		if len(call.Args) > 3 {
			schemaExpression = call.Args[3]
		}

		for _, argument := range call.Args {
			named, ok := argument.(*syntax.BinaryExpr)
			if !ok || named.Op != syntax.EQ {
				continue
			}

			key, ok := named.X.(*syntax.Ident)
			if !ok || key.Name != "schema" {
				continue
			}

			schemaExpression = named.Y
		}

		if schemaExpression == nil {
			return true
		}

		expression := unparenthesized(schemaExpression).value
		if identifier, ok := expression.(*syntax.Ident); ok {
			expression = unparenthesized(literals[identifier.Name]).value
		}

		if _, ok := expression.(*syntax.DictExpr); !ok || !isJSONLiteral(expression) {
			return true
		}

		if validated[expression] {
			return true
		}

		validated[expression] = true
		thread := &starlark.Thread{Name: "schema validation"}
		value, err := starlark.EvalExprOptions(options, thread, expression, nil)

		var encoded starlark.Value
		if err == nil {
			encoded, err = starlark.Call(thread, starjson.Module.Members["encode"], starlark.Tuple{value}, nil)
		}

		var schema jsonschema.Schema
		if err == nil {
			err = json.Unmarshal([]byte(encoded.(starlark.String)), &schema)
		}

		if err == nil {
			_, err = schema.Resolve(nil)
		}

		if err != nil {
			errSchema = fmt.Errorf("agent schema: %w", err)
			return false
		}

		return true
	})

	return errSchema
}

func isEffectfulDefault(expression syntax.Expr) bool {
	effectful := false

	syntax.Walk(expression, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok {
			return !effectful
		}

		name, ok := callName(call)
		if ok && (name == "worker" || name == "agent" || name == "parallel" || name == "pipeline" || name == "phase" || name == "print" || name == "fail") {
			effectful = true
		}

		return !effectful
	})

	return effectful
}

type normalizedExpression struct{ value syntax.Expr }

func unparenthesized(expression syntax.Expr) normalizedExpression {
	for {
		parenthesized, ok := expression.(*syntax.ParenExpr)
		if !ok {
			return normalizedExpression{value: expression}
		}

		expression = parenthesized.X
	}
}

func callName(call *syntax.CallExpr) (string, bool) {
	identifier, ok := unparenthesized(call.Fn).value.(*syntax.Ident)
	if !ok {
		return "", false
	}

	return identifier.Name, true
}

func isJSONLiteral(expression syntax.Expr) bool {
	expression = unparenthesized(expression).value

	switch expression := expression.(type) {
	case *syntax.Literal:
		return expression.Token == syntax.STRING || expression.Token == syntax.INT || expression.Token == syntax.FLOAT
	case *syntax.Ident:
		return expression.Name == "True" || expression.Name == "False" || expression.Name == "None"
	case *syntax.ListExpr:
		for _, item := range expression.List {
			if !isJSONLiteral(item) {
				return false
			}
		}

		return true
	case *syntax.DictExpr:
		for _, item := range expression.List {
			entry := item.(*syntax.DictEntry)

			key, ok := entry.Key.(*syntax.Literal)
			if !ok || key.Token != syntax.STRING || !isJSONLiteral(entry.Value) {
				return false
			}
		}

		return true
	case *syntax.UnaryExpr:
		literal, ok := expression.X.(*syntax.Literal)
		return ok && (expression.Op == syntax.PLUS || expression.Op == syntax.MINUS) && (literal.Token == syntax.INT || literal.Token == syntax.FLOAT)
	default:
		return false
	}
}

func validateWorker(call *syntax.CallExpr) (string, error) {
	workerModel := ""

	function, ok := callName(call)
	if !ok || function != "worker" {
		if ok {
			return "", fmt.Errorf("global value must be a literal, not a %s call", function)
		}

		return "", errors.New("top-level calls other than worker are not allowed")
	}

	arguments := make(map[string]syntax.Expr)

	for _, argument := range call.Args {
		named, ok := argument.(*syntax.BinaryExpr)
		if !ok || named.Op != syntax.EQ {
			return "", errors.New("worker arguments must be named literals")
		}

		name, ok := named.X.(*syntax.Ident)
		if !ok {
			return "", errors.New("worker arguments must be named literals")
		}

		if _, exists := arguments[name.Name]; exists {
			return "", fmt.Errorf("worker argument %s is duplicated", name.Name)
		}

		arguments[name.Name] = named.Y
	}

	for _, required := range []string{"name", "instructions"} {
		literal, ok := arguments[required].(*syntax.Literal)
		if !ok || literal.Token != syntax.STRING || strings.TrimSpace(literal.Value.(string)) == "" {
			return "", fmt.Errorf("worker %s must be a non-empty literal string", required)
		}
	}

	if model, exists := arguments["model"]; exists {
		switch model := model.(type) {
		case *syntax.Literal:
			if model.Token != syntax.STRING || strings.TrimSpace(model.Value.(string)) == "" {
				return "", errors.New("worker model must be a non-empty literal string or None")
			}

			workerModel = model.Value.(string)
		case *syntax.Ident:
			if model.Name != "None" {
				return "", errors.New("worker model must be a literal string or None")
			}
		default:
			return "", errors.New("worker model must be a literal string or None")
		}
	}

	if tools, exists := arguments["tools"]; exists {
		if err := validateWorkerTools(tools); err != nil {
			return "", err
		}
	}

	for name := range arguments {
		if name != "name" && name != "instructions" && name != "model" && name != "tools" {
			return "", fmt.Errorf("worker argument %s is not supported", name)
		}
	}

	return workerModel, nil
}

func validateWorkerTools(tools syntax.Expr) error {
	if none, ok := tools.(*syntax.Ident); ok && none.Name == "None" {
		return nil
	}

	list, ok := tools.(*syntax.ListExpr)
	if !ok {
		return errors.New("worker tools must be a literal list of strings or None")
	}

	seen := make(map[string]bool)

	for _, tool := range list.List {
		literal, ok := tool.(*syntax.Literal)
		if !ok || literal.Token != syntax.STRING {
			return errors.New("worker tools must be a literal list of strings or None")
		}

		name := literal.Value.(string)
		if name == "task" || seen[name] {
			return errors.New("worker tools must be unique and cannot include task")
		}

		seen[name] = true
	}

	return nil
}

func parseMeta(meta *syntax.DictExpr, filenameStem string) (name, description string, phases []string, err error) {
	fields := make(map[string]syntax.Expr)

	for _, item := range meta.List {
		entry := item.(*syntax.DictEntry)
		fields[entry.Key.(*syntax.Literal).Value.(string)] = entry.Value
	}

	nameValue, ok := fields["name"].(*syntax.Literal)
	if !ok || nameValue.Token != syntax.STRING || nameValue.Value.(string) == "" {
		return "", "", nil, errors.New("meta name must be a non-empty literal string")
	}

	name = nameValue.Value.(string)
	if name != filenameStem {
		return "", "", nil, fmt.Errorf("meta name %q must equal filename stem %q", name, filenameStem)
	}

	descriptionValue, ok := fields["description"].(*syntax.Literal)
	if !ok || descriptionValue.Token != syntax.STRING || descriptionValue.Value.(string) == "" {
		return "", "", nil, errors.New("meta description must be a non-empty literal string")
	}

	if phasesValue, exists := fields["phases"]; exists {
		list, ok := phasesValue.(*syntax.ListExpr)
		if !ok {
			return "", "", nil, errors.New("meta phases must be a literal list")
		}

		if len(list.List) > 100 {
			return "", "", nil, errors.New("meta must declare at most 100 phases")
		}

		seen := make(map[string]bool)

		for _, item := range list.List {
			literal, ok := item.(*syntax.Literal)
			if !ok || literal.Token != syntax.STRING || literal.Value.(string) == "" {
				return "", "", nil, errors.New("each phase must be a non-empty literal string")
			}

			phase := literal.Value.(string)
			if seen[phase] {
				return "", "", nil, fmt.Errorf("duplicate phase %q", phase)
			}

			seen[phase] = true
			phases = append(phases, phase)
		}
	}

	return name, descriptionValue.Value.(string), phases, nil
}

func validateNestedFanout(file *syntax.File, functions map[string]*syntax.DefStmt) error {
	var errNested error

	syntax.Walk(file, func(node syntax.Node) bool {
		if errNested != nil || node == nil {
			return false
		}

		call, ok := node.(*syntax.CallExpr)
		if !ok {
			return true
		}

		function, ok := callName(call)
		if !ok || (function != "parallel" && function != "pipeline") {
			return true
		}

		var callbackArgument syntax.Expr

		positional := 0

		for _, argument := range call.Args {
			keyword := ""

			if named, ok := argument.(*syntax.BinaryExpr); ok && named.Op == syntax.EQ {
				if name, ok := named.X.(*syntax.Ident); ok {
					keyword = name.Name
					argument = named.Y
				}
			} else {
				positional++
			}

			if function == "parallel" && (keyword == "callables" || keyword == "" && positional == 1) ||
				function == "pipeline" && (keyword == "fn" || keyword == "" && positional == 2) {
				callbackArgument = argument
			}
		}

		callbackArgument = unparenthesized(callbackArgument).value

		var callbacks []syntax.Expr

		if function == "parallel" {
			if list, ok := callbackArgument.(*syntax.ListExpr); ok {
				callbacks = list.List
			}
		} else if callbackArgument != nil {
			callbacks = append(callbacks, callbackArgument)
		}

		for _, callback := range callbacks {
			if callbackHasFanout(callback, functions, make(map[string]bool)) {
				errNested = errors.New("nested fan-out through parallel or pipeline callbacks is not allowed")
				return false
			}
		}

		return true
	})

	return errNested
}

func callbackHasFanout(callback syntax.Expr, functions map[string]*syntax.DefStmt, visited map[string]bool) bool {
	callback = unparenthesized(callback).value

	var root syntax.Node = callback
	if identifier, ok := callback.(*syntax.Ident); ok {
		if visited[identifier.Name] || functions[identifier.Name] == nil {
			return false
		}

		visited[identifier.Name] = true
		root = functions[identifier.Name]
	}

	found := false

	syntax.Walk(root, func(node syntax.Node) bool {
		if found || node == nil {
			return false
		}

		call, ok := node.(*syntax.CallExpr)
		if !ok {
			return true
		}

		identifier, ok := callName(call)
		if !ok {
			return true
		}

		if identifier == "parallel" || identifier == "pipeline" {
			found = true
			return false
		}

		if function := functions[identifier]; function != nil && !visited[identifier] {
			if callbackHasFanout(function.Name, functions, visited) {
				found = true
				return false
			}
		}

		return true
	})

	return found
}
