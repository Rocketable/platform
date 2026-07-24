package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"go.starlark.net/starlark"
)

func TestLoadValidDefinitions(t *testing.T) {
	tests := []struct {
		name         string
		file         string
		source       string
		description  string
		phases       []string
		workerModels []string
	}{
		{
			name:        "minimal",
			file:        "minimal.star",
			source:      "meta = {\"name\": \"minimal\", \"description\": \"Minimal workflow\"}\n\ndef main(args):\n    return args\n",
			description: "Minimal workflow",
		},
		{
			name: "full-definition",
			file: "full-definition.star",
			source: `meta = {
    "name": "full-definition",
    "description": "Full workflow",
    "phases": ["discover", "verify"],
}
schema = {
    "type": "object",
    "required": ["answer"],
    "properties": {"answer": {"type": "string"}},
    "additionalProperties": False,
}
researcher = worker(
    name = "researcher",
    instructions = "Find evidence.",
    model = "coding-low",
    tools = ["read", "glob", "grep"],
)

def main(args):
    return phase("discover", lambda: agent(args, worker = researcher, schema = schema))
`,
			description:  "Full workflow",
			phases:       []string{"discover", "verify"},
			workerModels: []string{"coding-low"},
		},
		{
			name: "parenthesized-worker",
			file: "parenthesized-worker.star",
			source: `meta = {"name": "parenthesized-worker", "description": "Parenthesized worker"}
researcher = (worker)(name = "researcher", instructions = "Find evidence.")
def main(args):
    return args
`,
			description: "Parenthesized worker",
		},
		{
			name:        "positional-schema",
			file:        "positional-schema.star",
			source:      "meta = {\"name\": \"positional-schema\", \"description\": \"Positional schema\"}\ndef main(args): return agent(args, None, \"result\", {\"type\": \"string\"})\n",
			description: "Positional schema",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := workflowRoot(t)
			writeWorkflow(t, root, tt.file, tt.source)

			definitions, err := Load(root, "runtime")
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}

			definition := definitions[tt.name]
			if definition == nil {
				t.Fatalf("Load()[%q] = nil", tt.name)
			}

			if definition.Name != tt.name || definition.Description != tt.description {
				t.Errorf("Load()[%q] metadata = (%q, %q), want (%q, %q)", tt.name, definition.Name, definition.Description, tt.name, tt.description)
			}

			if !slices.Equal(definition.Phases, tt.phases) {
				t.Errorf("Load()[%q].Phases = %v, want %v", tt.name, definition.Phases, tt.phases)
			}

			if !slices.Equal(definition.WorkerModels, tt.workerModels) {
				t.Errorf("Load()[%q].WorkerModels = %v, want %v", tt.name, definition.WorkerModels, tt.workerModels)
			}

			if definition.program == nil {
				t.Errorf("Load()[%q].program = nil", tt.name)
			}
		})
	}
}

func TestLoadFiles(t *testing.T) {
	t.Run("stats and reads the opened file", func(t *testing.T) {
		root := workflowRoot(t)
		writeWorkflow(t, root, "source", `meta = {"name": "linked", "description": "Linked workflow"}
def main(args): return args
`)

		if err := root.Symlink("source", "runtime/workflows/linked.star"); err != nil {
			t.Fatal(err)
		}

		definitions, err := Load(root, "runtime")
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}

		if definitions["linked"] == nil {
			t.Fatal("Load()[\"linked\"] = nil, want definition from opened regular file")
		}
	})

	t.Run("rejects symlink escaping workflows directory", func(t *testing.T) {
		root := workflowRoot(t)
		if err := root.WriteFile("outside.star", []byte(`meta = {"name": "escaped", "description": "Escaped workflow"}
def main(args): return args
`), 0o644); err != nil {
			t.Fatal(err)
		}

		if err := root.Symlink("../../outside.star", "runtime/workflows/escaped.star"); err != nil {
			t.Fatal(err)
		}

		if _, err := Load(root, "runtime"); err == nil {
			t.Fatal("Load() error = nil, want workflows subtree escape rejection")
		}
	})

	t.Run("multiple files", func(t *testing.T) {
		root := workflowRoot(t)
		for _, name := range []string{"second", "first"} {
			writeWorkflow(t, root, name+".star", `meta = {"name": "`+name+`", "description": "`+name+`"}
def main(args): return args
`)
		}

		definitions, err := Load(root, "runtime")
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}

		if len(definitions) != 2 || definitions["first"] == nil || definitions["second"] == nil {
			t.Fatalf("Load() definitions = %v, want first and second", definitions)
		}
	})

	t.Run("deterministic filename order", func(t *testing.T) {
		root := workflowRoot(t)
		writeWorkflow(t, root, "z-invalid.star", `meta = {"name": "wrong", "description": "z"}
def main(args): return args
`)
		writeWorkflow(t, root, "a-invalid.star", `meta = {"name": "wrong", "description": "a"}
def main(args): return args
`)

		_, err := Load(root, "runtime")
		if err == nil || !strings.Contains(err.Error(), "a-invalid.star") {
			t.Fatalf("Load() error = %v, want first filename a-invalid.star", err)
		}
	})

	t.Run("missing directory", func(t *testing.T) {
		root, err := os.OpenRoot(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() {
			if errClose := root.Close(); errClose != nil {
				t.Errorf("close root: %v", errClose)
			}
		})

		definitions, err := Load(root, "runtime")
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}

		if len(definitions) != 0 {
			t.Fatalf("Load() returned %d definitions, want 0", len(definitions))
		}
	})

	t.Run("ignores nested and non-regular entries", func(t *testing.T) {
		root := workflowRoot(t)
		if err := root.MkdirAll("runtime/workflows/Old-Drafts.star", 0o755); err != nil {
			t.Fatal(err)
		}

		if err := root.WriteFile("runtime/workflows/Old-Drafts.star/hidden.star", []byte("invalid"), 0o644); err != nil {
			t.Fatal(err)
		}

		definitions, err := Load(root, "runtime")
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}

		if len(definitions) != 0 {
			t.Fatalf("Load() returned %d definitions, want 0", len(definitions))
		}
	})
}

func TestLoadRejectsInvalidDefinitions(t *testing.T) {
	validMeta := `meta = {"name": "test", "description": "Test workflow"}`
	validMain := "def main(args):\n    return args\n"
	tests := []struct {
		name   string
		file   string
		source string
		want   string
	}{
		{name: "invalid filename", file: "Bad.star", source: validMeta + "\n" + validMain, want: "workflow name"},
		{name: "overlong filename", file: strings.Repeat("a", 65) + ".star", source: validMeta + "\n" + validMain, want: "workflow name"},
		{name: "name stem mismatch", file: "test.star", source: `meta = {"name": "other", "description": "Test"}` + "\n" + validMain, want: "filename"},
		{name: "missing meta", file: "test.star", source: validMain, want: "meta"},
		{name: "dynamic meta", file: "test.star", source: "name = \"test\"\nmeta = {\"name\": name, \"description\": \"Test\"}\n" + validMain, want: "meta"},
		{name: "meta not dict", file: "test.star", source: "meta = \"test\"\n" + validMain, want: "meta"},
		{name: "missing meta name", file: "test.star", source: `meta = {"description": "Test"}` + "\n" + validMain, want: "name"},
		{name: "empty meta name", file: "test.star", source: `meta = {"name": "", "description": "Test"}` + "\n" + validMain, want: "name"},
		{name: "non-string meta name", file: "test.star", source: `meta = {"name": 1, "description": "Test"}` + "\n" + validMain, want: "name"},
		{name: "missing description", file: "test.star", source: `meta = {"name": "test"}` + "\n" + validMain, want: "description"},
		{name: "empty description", file: "test.star", source: `meta = {"name": "test", "description": ""}` + "\n" + validMain, want: "description"},
		{name: "non-string description", file: "test.star", source: `meta = {"name": "test", "description": []}` + "\n" + validMain, want: "description"},
		{name: "phases not list", file: "test.star", source: `meta = {"name": "test", "description": "Test", "phases": "one"}` + "\n" + validMain, want: "phases"},
		{name: "empty phase", file: "test.star", source: `meta = {"name": "test", "description": "Test", "phases": [""]}` + "\n" + validMain, want: "phase"},
		{name: "non-string phase", file: "test.star", source: `meta = {"name": "test", "description": "Test", "phases": [1]}` + "\n" + validMain, want: "phase"},
		{name: "duplicate phases", file: "test.star", source: `meta = {"name": "test", "description": "Test", "phases": ["one", "one"]}` + "\n" + validMain, want: "duplicate phase"},
		{name: "missing main", file: "test.star", source: validMeta, want: "main"},
		{name: "main not callable", file: "test.star", source: validMeta + "\nmain = 1\n", want: "main"},
		{name: "main no parameters", file: "test.star", source: validMeta + "\ndef main():\n    pass\n", want: "main"},
		{name: "main two parameters", file: "test.star", source: validMeta + "\ndef main(a, b):\n    pass\n", want: "main"},
		{name: "main default parameter", file: "test.star", source: validMeta + "\ndef main(args = \"\"):\n    pass\n", want: "main"},
		{name: "main variadic", file: "test.star", source: validMeta + "\ndef main(*args):\n    pass\n", want: "main"},
		{name: "main keyword parameters", file: "test.star", source: validMeta + "\ndef main(**kwargs):\n    pass\n", want: "main"},
		{name: "top-level agent", file: "test.star", source: validMeta + "\nx = agent(\"prompt\")\n" + validMain, want: "agent"},
		{name: "top-level parallel", file: "test.star", source: validMeta + "\nx = parallel([])\n" + validMain, want: "parallel"},
		{name: "top-level pipeline", file: "test.star", source: validMeta + "\nx = pipeline([], lambda x: x)\n" + validMain, want: "pipeline"},
		{name: "top-level phase", file: "test.star", source: validMeta + "\nx = phase(\"run\", lambda: None)\n" + validMain, want: "phase"},
		{name: "top-level print", file: "test.star", source: validMeta + "\nx = print(\"no\")\n" + validMain, want: "print"},
		{name: "function print", file: "test.star", source: validMeta + "\ndef main(args):\n    print(args)\n", want: "print"},
		{name: "parenthesized function print", file: "test.star", source: validMeta + "\ndef main(args):\n    (print)(\"x\")\n", want: "print"},
		{name: "expression statement", file: "test.star", source: validMeta + "\n1\n" + validMain, want: "top-level"},
		{name: "comprehension", file: "test.star", source: validMeta + "\nvalues = [x for x in [1]]\n" + validMain, want: "literal"},
		{name: "load", file: "test.star", source: "load(\"other.star\", \"x\")\n" + validMeta + "\n" + validMain, want: "load"},
		{name: "top-level control", file: "test.star", source: "if True:\n    meta = {}\n" + validMain, want: "top-level"},
		{name: "effectful default", file: "test.star", source: validMeta + "\ndef helper(value = agent(\"prompt\")):\n    return value\n\n" + validMain, want: "default"},
		{name: "fail default", file: "test.star", source: validMeta + "\ndef helper(value = fail(\"no\")):\n    return value\n\n" + validMain, want: "effectful default"},
		{name: "dynamic worker name", file: "test.star", source: validMeta + "\nworker_name = \"researcher\"\nw = worker(name = worker_name, instructions = \"Find\")\n" + validMain, want: "worker"},
		{name: "whitespace worker name", file: "test.star", source: validMeta + "\nw = worker(name = \" \", instructions = \"Find\")\n" + validMain, want: "name"},
		{name: "missing worker instructions", file: "test.star", source: validMeta + "\nw = worker(name = \"researcher\")\n" + validMain, want: "instructions"},
		{name: "whitespace worker instructions", file: "test.star", source: validMeta + "\nw = worker(name = \"researcher\", instructions = \" \")\n" + validMain, want: "instructions"},
		{name: "dynamic worker instructions", file: "test.star", source: validMeta + "\nw = worker(name = \"researcher\", instructions = \"Find \" + \"facts\")\n" + validMain, want: "worker"},
		{name: "empty worker model", file: "test.star", source: validMeta + "\nw = worker(name = \"researcher\", instructions = \"Find\", model = \"\")\n" + validMain, want: "model"},
		{name: "invalid worker model", file: "test.star", source: validMeta + "\nw = worker(name = \"researcher\", instructions = \"Find\", model = 1)\n" + validMain, want: "model"},
		{name: "invalid worker tools", file: "test.star", source: validMeta + "\nw = worker(name = \"researcher\", instructions = \"Find\", tools = [\"read\", 1])\n" + validMain, want: "tools"},
		{name: "duplicate worker tools", file: "test.star", source: validMeta + "\nw = worker(name = \"researcher\", instructions = \"Find\", tools = [\"read\", \"read\"])\n" + validMain, want: "unique"},
		{name: "task worker tool", file: "test.star", source: validMeta + "\nw = worker(name = \"researcher\", instructions = \"Find\", tools = [\"task\"])\n" + validMain, want: "cannot include task"},
		{name: "dynamic global", file: "test.star", source: validMeta + "\nschema = dict(type = \"object\")\n" + validMain, want: "literal"},
		{name: "invalid inline agent schema", file: "test.star", source: validMeta + "\ndef main(args):\n    return agent(args, schema = {\"pattern\": \"[\"})\n", want: "schema"},
		{name: "invalid global agent schema", file: "test.star", source: validMeta + "\nschema = {\"pattern\": \"[\"}\ndef main(args):\n    return agent(args, schema = schema)\n", want: "schema"},
		{name: "invalid positional inline agent schema", file: "test.star", source: validMeta + "\ndef main(args):\n    return agent(args, None, \"result\", {\"pattern\": \"[\"})\n", want: "schema"},
		{name: "invalid positional global agent schema", file: "test.star", source: validMeta + "\nschema = {\"pattern\": \"[\"}\ndef main(args):\n    return agent(args, None, \"result\", schema)\n", want: "schema"},
		{name: "non-JSON global", file: "test.star", source: validMeta + "\nvalue = (1, 2)\n" + validMain, want: "literal"},
		{name: "worker alias in function", file: "test.star", source: validMeta + "\ndef main(args):\n    make = worker\n    return make(name = \"researcher\", instructions = \"Find\", tools = [1])\n", want: "worker"},
		{name: "direct lambda nested fan-out", file: "test.star", source: validMeta + "\ndef main(args):\n    return parallel([lambda: pipeline([], lambda x: x)])\n", want: "nested fan-out"},
		{name: "parenthesized callee nested fan-out", file: "test.star", source: validMeta + "\ndef main(args):\n    return (parallel)([lambda: parallel([])])\n", want: "nested fan-out"},
		{name: "keyword parallel nested fan-out", file: "test.star", source: validMeta + "\ndef main(args):\n    return parallel(callables = [lambda: parallel([])])\n", want: "nested fan-out"},
		{name: "keyword pipeline nested fan-out", file: "test.star", source: validMeta + "\ndef main(args):\n    return pipeline(items = [], fn = lambda x: parallel([]))\n", want: "nested fan-out"},
		{name: "parenthesized parallel nested fan-out", file: "test.star", source: validMeta + "\ndef main(args):\n    return parallel(([lambda: parallel([])]))\n", want: "nested fan-out"},
		{name: "parenthesized pipeline nested fan-out", file: "test.star", source: validMeta + "\ndef main(args):\n    return pipeline([], (lambda x: parallel([])))\n", want: "nested fan-out"},
		{name: "named callback nested fan-out", file: "test.star", source: validMeta + "\ndef branch():\n    return parallel([])\n\ndef main(args):\n    return parallel([branch])\n", want: "nested fan-out"},
		{name: "parenthesized named callback nested fan-out", file: "test.star", source: validMeta + "\ndef branch():\n    return parallel([])\n\ndef main(args):\n    return parallel([(branch)])\n", want: "nested fan-out"},
		{name: "helper-indirected nested fan-out", file: "test.star", source: validMeta + "\ndef helper():\n    return pipeline([], lambda x: x)\n\ndef branch():\n    return helper()\n\ndef main(args):\n    return parallel([branch])\n", want: "nested fan-out"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := workflowRoot(t)
			writeWorkflow(t, root, tt.file, tt.source)

			_, err := Load(root, "runtime")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestCompileDefinitionRejectsMoreThanOneHundredDeclaredPhases(t *testing.T) {
	phases := make([]string, 101)
	for i := range phases {
		phases[i] = fmt.Sprintf("phase-%d", i)
	}

	encoded, err := json.Marshal(phases)
	if err != nil {
		t.Fatal(err)
	}

	source := `meta = {"name": "test", "description": "Test", "phases": ` + string(encoded) + "}\ndef main(args): return None\n"

	_, err = compileDefinition("test.star", "test", []byte(source))
	if err == nil || !strings.Contains(err.Error(), "at most 100 phases") {
		t.Fatalf("compileDefinition() error = %v, want 100-phase limit", err)
	}
}

func TestLoadAllowsPureComputedFunctionDefault(t *testing.T) {
	root := workflowRoot(t)
	writeWorkflow(t, root, "test.star", `meta = {"name": "test", "description": "Test"}
def helper(value = 1 + 2):
    return value
def main(args):
    return helper()
`)

	if _, err := Load(root, "runtime"); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadDialectOptions(t *testing.T) {
	valid := []struct {
		name string
		body string
	}{
		{name: "while", body: "    while False:\n        pass\n    return args"},
		{name: "set", body: "    values = set([1, 2])\n    return values"},
	}
	for _, tt := range valid {
		t.Run(tt.name+" enabled", func(t *testing.T) {
			root := workflowRoot(t)
			writeWorkflow(t, root, "test.star", "meta = {\"name\": \"test\", \"description\": \"Test\"}\ndef main(args):\n"+tt.body+"\n")

			if _, err := Load(root, "runtime"); err != nil {
				t.Fatalf("Load() error = %v", err)
			}
		})
	}

	invalid := []struct {
		name   string
		source string
		want   string
	}{
		{name: "global reassignment disabled", source: "meta = {\"name\": \"test\", \"description\": \"Test\"}\nvalue = 1\nvalue = 2\ndef main(args):\n    return value\n", want: "reassign"},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			root := workflowRoot(t)
			writeWorkflow(t, root, "test.star", tt.source)

			_, err := Load(root, "runtime")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want containing %q", err, tt.want)
			}
		})
	}

	t.Run("recursion disabled", func(t *testing.T) {
		root := workflowRoot(t)
		writeWorkflow(t, root, "test.star", "meta = {\"name\": \"test\", \"description\": \"Test\"}\ndef helper():\n    return helper()\ndef main(args):\n    return helper()\n")

		definitions, err := Load(root, "runtime")
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}

		thread := &starlark.Thread{Name: "recursion test"}

		globals, errInit := definitions["test"].program.Init(thread, nil)
		if errInit != nil {
			t.Fatalf("Program.Init() error = %v", errInit)
		}

		_, errCall := starlark.Call(thread, globals["main"], starlark.Tuple{starlark.String("")}, nil)
		if errCall == nil || !strings.Contains(errCall.Error(), "recursively") {
			t.Fatalf("main() error = %v, want recursion rejection", errCall)
		}
	})
}

func TestLoadRejectsValidationStepExhaustion(t *testing.T) {
	root := workflowRoot(t)
	source := `meta = {"name": "test", "description": "Test"}
large = [` + strings.Repeat("0,", 100_000) + `]
def main(args):
    return args
`
	writeWorkflow(t, root, "test.star", source)

	_, err := Load(root, "runtime")
	if err == nil || !strings.Contains(err.Error(), "step") {
		t.Fatalf("Load() error = %v, want step exhaustion", err)
	}
}

func workflowRoot(t *testing.T) *os.Root {
	t.Helper()

	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if errClose := root.Close(); errClose != nil {
			t.Errorf("close root: %v", errClose)
		}
	})

	if err := root.MkdirAll("runtime/workflows", 0o755); err != nil {
		t.Fatal(err)
	}

	return root
}

func writeWorkflow(t *testing.T, root *os.Root, name, source string) {
	t.Helper()

	if err := root.WriteFile("runtime/workflows/"+name, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
}
