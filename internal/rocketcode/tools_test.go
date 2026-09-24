package rocketcode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
)

// Command parity: OpenCode packages/core/src/config/plugin/command.ts and
// https://opencode.ai/docs/commands/#arguments (V2 command convention).
func TestRenderSkillArguments(t *testing.T) {
	for _, tt := range []struct {
		name, template, arguments, want string
		direct                          bool
	}{
		{"raw", "<$ARGUMENTS>", "  one\t 'two three'\n", "<  one\t 'two three'\n>", true},
		{"quoted positions", "$1 / $2 / $3", `"one two" 'three four' five six`, "one two / three four / five six", true},
		{"missing", "$1 / $3", "one", "one / ", true},
		{"repeated highest", "$2 / $1 / $2", "one two three", "two three / one / two three", true},
		{"single highest", "$1", "one two three", "one two three", true},
		{"prefix and raw", "$1 / $10 / $ARGUMENTS", "$1 b c d e f g h i j k", "$1 / j k / $1 b c d e f g h i j k", true},
		{"nonrecursive", "$1 / $ARGUMENTS", "$ARGUMENTS", "$ARGUMENTS / $ARGUMENTS", true},
		{"zero literal", "$0 / $1", "one two", "$0 / one two", true},
		{"zero does not consume", "$0", "one", "$0\n\none", true},
		{"empty arguments", "$1 / $2 / $ARGUMENTS", "", " /  / ", true},
		{"empty quoted position", "$1 / $2", `"" next`, " / next", true},
		{"direct append", "body", "raw  'text'", "body\n\nraw  'text'", true},
		{"model no append", "body", "unused", "body", false},
		{"empty no append", "body", "", "body", true},
		{"model positions", "$1 / $2", "one two three", "one / two three", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, renderSkillArguments(tt.template, tt.arguments, tt.direct))
		})
	}
}

func TestGlobGrepPermissionSubjects(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	outputDir := filepath.Join(dir, ".tmp", "shell-tmp")
	require.NoError(t, root.MkdirAll(filepath.Join(".tmp", "shell-tmp"), 0o755))

	tools := newSandboxedTools(root, testShellTempConfig(t, root, outputDir), nil, DefaultShellCommand)

	globSubjects, err := tools["glob"].Subjects(json.RawMessage(`{"pattern":"**/*.go","path":"src"}`))
	require.NoError(t, err)
	require.Equal(t, []string{"src"}, globSubjects)

	globRootSubjects, err := tools["glob"].Subjects(json.RawMessage(`{"pattern":"**/*.go"}`))
	require.NoError(t, err)
	require.Equal(t, []string{"."}, globRootSubjects)

	grepSubjects, err := tools["grep"].Subjects(json.RawMessage(`{"pattern":"func Test","path":"src","include":"*_test.go"}`))
	require.NoError(t, err)
	require.Equal(t, []string{"func Test"}, grepSubjects)
}

func TestWebFetchPermissionSubjectsMatchOpenCode(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	outputDir := filepath.Join(dir, ".tmp", "shell-tmp")
	require.NoError(t, root.MkdirAll(filepath.Join(".tmp", "shell-tmp"), 0o755))

	tools := newSandboxedTools(root, testShellTempConfig(t, root, outputDir), nil, DefaultShellCommand)

	subjects, err := tools["webfetch"].Subjects(json.RawMessage(`{"url":"https://docs.example/path?q=1","format":"markdown"}`))

	require.NoError(t, err)
	require.Equal(t, []string{"https://docs.example/path?q=1"}, subjects)
}

func TestWebSearchPermissionIsCoarse(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	outputDir := filepath.Join(dir, ".tmp", "shell-tmp")
	require.NoError(t, root.MkdirAll(filepath.Join(".tmp", "shell-tmp"), 0o755))

	tools := newSandboxedTools(root, testShellTempConfig(t, root, outputDir), nil, DefaultShellCommand)

	loop := &looper{Permissions: PermissionSet{Buckets: []PermissionBucket{{Name: "websearch", Rules: []PermissionRule{{Pattern: "*", Action: permissionDeny}}}}}}
	tool := tools["websearch"]
	decision, err := loop.permissionDecision("websearch", &tool, json.RawMessage(`{}`))

	require.NoError(t, err)
	require.True(t, decision.denied)
	require.Contains(t, decision.message, `subject "*"`)
	require.Equal(t, "web_search", *tools["websearch"].Hosted.GetType())
}

func TestBashPermissionsCheckWholeScript(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.Mkdir(".tmp", 0o755))
	tools := newSandboxedTools(root, testShellTempConfig(t, root, filepath.Join(root.Name(), ".tmp")), nil, DefaultShellCommand)
	tool := tools["bash"]

	loop := &looper{Permissions: parsePermissionYAML(t, `bash: {"scripts/cmd *": allow}`), Tools: tools}
	for _, tt := range []struct {
		command string
		denied  bool
	}{
		{"scripts/cmd arg", false},
		{"\u00a0scripts/cmd arg", true},
		{"\rscripts/cmd arg", true},
		{"scripts/cmd arg\r\nforbidden", true},
		{"scripts/cmd\x00 arg", true},
		{"scripts/cmd first && scripts/cmd second", false},
		{"scripts/cmd first || scripts/cmd second", false},
		{"scripts/cmd first; scripts/cmd second", false},
		{"scripts/cmd first\nscripts/cmd second", false},
		{"scripts/cmd first | scripts/cmd second", false},
		{"scripts/cmd 'literal; export FOO=bar'", false},
		{`scripts/cmd "literal * ? ~ {x,y}"`, false},
		{`scripts/cmd \*`, false},
		{`scripts/cmd \{a,b\}`, false},
		{"scripts/cmd {}", false},
		{"scripts/cmd []", false},
		{"scripts/cmd [!]", false},
		{"scripts/cmd [!!]", true},
		{"scripts/cmd [a]", true},
		{"scripts/cmd [~]", true},
		{`scripts/cmd ["a"]`, true},
		{`scripts/cmd [""]`, false},
		{"scripts/cmd a~b", false},
		{"scripts/cmd FOO=~", true},
		{"scripts/cmd F\\\nOO=~", true},
		{"scripts/cmd FOO=bar:~", true},
		{`scripts/cmd FOO="bar":~`, true},
		{`scripts/cmd FOO='~'`, false},
		{`scripts/cmd FOO=bar\:~`, false},
		{"scripts/cmd arg; forbidden", true},
		{"scripts/cmd arg && forbidden", true},
		{"scripts/cmd arg || forbidden", true},
		{"scripts/cmd arg | forbidden", true},
		{"export FOO=bar; scripts/cmd arg", true},
		{"scripts/cmd arg; export FOO=bar", true},
		{"FOO=bar; scripts/cmd arg", true},
		{"scripts/cmd arg > output", true},
		{"scripts/cmd arg < input", true},
		{"scripts/cmd arg 2>&1", true},
		{"scripts/cmd arg <<< data", true},
		{"scripts/cmd arg <<EOF\n$(forbidden)\nEOF", true},
		{"> output; scripts/cmd arg", true},
		{"scripts/cmd $(forbidden)", true},
		{"scripts/cmd `forbidden`", true},
		{"scripts/cmd <(forbidden)", true},
		{"scripts/cmd $(< input)", true},
		{"scripts/cmd ${FOO:=bar}", true},
		{"scripts/cmd $FOO", true},
		{"scripts/cmd \"$FOO\"", true},
		{"scripts/cmd *", true},
		{"scripts/cmd ~/file", true},
		{"scripts/cmd {first,second}", true},
		{"scripts/cmd =forbidden", false},
		{"scripts/cmd $'escaped'", true},
		{"scripts/cmd $\"translated\"", true},
		{"scripts/cmd \\a", false},
		{"scripts/cmd $((FOO=1))", true},
		{"((FOO=1)); scripts/cmd arg", true},
		{"let FOO=1; scripts/cmd arg", true},
		{"[[ -f input ]]; scripts/cmd arg", true},
		{"for FOO in bar; do scripts/cmd arg; done", true},
		{"if scripts/cmd arg; then scripts/cmd next; fi", true},
		{"i\\\nf scripts/cmd arg; then scripts/cmd next; fi", true},
		{"while scripts/cmd arg; do scripts/cmd next; done", true},
		{"case foo in foo) scripts/cmd arg;; esac", true},
		{"function f() { scripts/cmd arg; }; scripts/cmd next", true},
		{"(scripts/cmd arg)", true},
		{"{ scripts/cmd arg; }", true},
		{"time scripts/cmd arg", true},
		{"coproc scripts/cmd arg", true},
		{"scripts/cmd arg &", true},
		{"scripts/cmd arg |& scripts/cmd next", true},
		{"! scripts/cmd arg", true},
		{"scripts/cmd arg; export FOO=(", true},
	} {
		t.Run(tt.command, func(t *testing.T) {
			raw, err := json.Marshal(bashParams{Command: tt.command})
			require.NoError(t, err)
			decision, err := loop.permissionDecision("bash", &tool, raw)
			require.NoError(t, err)
			require.Equal(t, tt.denied, decision.denied, decision.message)
		})
	}

	loop.Permissions = parsePermissionYAML(t, `bash: allow`)

	for _, command := range []string{"$COMMAND arg", "$(forbidden) arg", "scripts/* arg", "scripts/cmd; export FOO=(", "\rscripts/cmd arg", "scripts/cmd\x00 arg"} {
		raw, err := json.Marshal(bashParams{Command: command})
		require.NoError(t, err)
		outputs, _, err := loop.dispatchToolCalls(t.Context(), responseWithFunctionCalls("response", []responses.ResponseFunctionToolCall{testFunctionCall("tool", "call", "bash", string(raw))}), nil, nil)
		require.NoError(t, err)
		require.Contains(t, outputs[0].Result.Output, "tool call denied", command)
	}

	_, err = root.Stat("output")
	require.ErrorIs(t, err, os.ErrNotExist)

	for _, tt := range []struct {
		name, command, rules string
		denied               bool
	}{
		{"separate grants", "export FOO=bar; scripts/cmd arg", `{"export FOO=bar": allow, "scripts/cmd arg": allow}`, false},
		{"whole script grant", "export FOO=bar; scripts/cmd arg", `{"export FOO=bar; scripts/cmd arg": allow}`, false},
		{"wildcard script grant", "export FOO=bar; scripts/cmd arg", `{"export FOO=*; scripts/cmd *": allow}`, false},
		{"wildcard values and arguments", "export FOO=baz; scripts/cmd first second", `{"export FOO=*; scripts/cmd *": allow}`, false},
		{"wildcard empty value and arguments", "export FOO=; scripts/cmd", `{"export FOO=*; scripts/cmd *": allow}`, false},
		{"wildcard script denial last", "export FOO=bar; scripts/cmd arg", `{"*": allow, "export FOO=*; scripts/cmd *": deny}`, true},
		{"wildcard script grant last", "export FOO=bar; scripts/cmd arg", `{"export FOO=*": deny, "export FOO=*; scripts/cmd *": allow}`, false},
		{"wildcard component denial last", "export FOO=bar; scripts/cmd arg", `{"export FOO=*; scripts/cmd *": allow, "scripts/cmd *": deny}`, true},
		{"wildcard component grants last", "export FOO=bar; scripts/cmd arg", `{"export FOO=*; scripts/cmd *": deny, "export FOO=*": allow, "scripts/cmd *": allow}`, false},
		{"wildcard cannot swallow trailing command", "export FOO=bar; scripts/cmd arg; forbidden", `{"export FOO=*; scripts/cmd *": allow}`, true},
		{"wildcard cannot swallow leading command", "export FOO=bar; forbidden; scripts/cmd arg", `{"export FOO=*; scripts/cmd *": allow}`, true},
		{"wildcard cannot swallow redirect", "export FOO=bar; scripts/cmd arg > output", `{"export FOO=*; scripts/cmd *": allow}`, true},
		{"wildcard redirect target", "export FOO=bar; scripts/cmd arg > output", `{"export FOO=*; scripts/cmd * > *": allow}`, false},
		{"wildcard cannot change redirect operator", "export FOO=bar; scripts/cmd arg >> output", `{"export FOO=*; scripts/cmd * > *": allow}`, true},
		{"component wildcard cannot change redirect operator", "scripts/cmd arg >> output", `{"scripts/cmd *": allow, ">*": allow}`, true},
		{"wildcard cannot swallow substitution", "export FOO=$(forbidden); scripts/cmd arg", `{"export FOO=*; scripts/cmd *": allow}`, true},
		{"wildcard cannot swallow nested argument", "export FOO=bar; scripts/cmd $(forbidden)", `{"export FOO=*; scripts/cmd *": allow}`, true},
		{"wildcard cannot swallow background job", "export FOO=bar; scripts/cmd arg &", `{"export FOO=*; scripts/cmd *": allow}`, true},
		{"quoted separators stay data", "export FOO='bar; literal'; scripts/cmd 'arg; literal'", `{"export FOO=*; scripts/cmd *": allow}`, false},
		{"whole pattern cannot match quoted separator alone", "export FOO='bar; scripts/cmd fake'", `{"export FOO=*; scripts/cmd *": allow}`, true},
		{"quoted separator cannot hide wrong command", "export FOO='bar; scripts/cmd fake'; forbidden", `{"export FOO=*; scripts/cmd *": allow}`, true},
		{"quoted separator cannot hide pipeline", "export FOO=bar | scripts/cmd 'arg; scripts/cmd fake'", `{"export FOO=*; scripts/cmd *": allow}`, true},
		{"wildcard retains declaration spelling", "declare -x FOO=bar; scripts/cmd arg", `{"export FOO=*; scripts/cmd *": allow}`, true},
		{"whole script denial last", "export FOO=bar; scripts/cmd arg", `{"export FOO=bar": allow, "scripts/cmd arg": allow, "export FOO=bar; scripts/cmd arg": deny}`, true},
		{"whole script grant last", "export FOO=bar; scripts/cmd arg", `{"export FOO=bar": deny, "export FOO=bar; scripts/cmd arg": allow}`, false},
		{"component denial last", "export FOO=bar; scripts/cmd arg", `{"export FOO=bar; scripts/cmd arg": allow, "export FOO=bar": deny}`, true},
		{"components grant last", "export FOO=bar; scripts/cmd arg", `{"export FOO=bar; scripts/cmd arg": deny, "export FOO=bar": allow, "scripts/cmd arg": allow}`, false},
		{"one component grant insufficient", "export FOO=bar; scripts/cmd arg", `{"export FOO=bar; scripts/cmd arg": deny, "scripts/cmd arg": allow}`, true},
		{"no export synonym", "declare -x FOO=bar; scripts/cmd arg", `{"export FOO=bar": allow, "scripts/cmd arg": allow}`, true},
		{"explicit declare grant", "declare -x FOO=bar; scripts/cmd arg", `{"declare -x FOO=bar": allow, "scripts/cmd arg": allow}`, false},
		{"assignment and command", "FOO=bar scripts/cmd arg", `{"FOO=bar": allow, "scripts/cmd arg": allow}`, false},
		{"wildcard assignment and command", "FOO=bar scripts/cmd arg", `{"FOO=* scripts/cmd *": allow}`, false},
		{"assignment cannot impersonate prefixed command", "FOO='bar scripts/cmd fake'", `{"FOO=* scripts/cmd *": allow}`, true},
		{"assignment cannot impersonate two assignments", "FOO='bar BAR=fake'", `{"FOO=* BAR=*": allow}`, true},
		{"assignment cannot hide command", "FOO=bar forbidden", `{"FOO=*": allow}`, true},
		{"redirect granted separately", "scripts/cmd arg > output", `{"scripts/cmd arg": allow, ">output": allow}`, false},
		{"compound cannot hide child", "for FOO in bar; do forbidden; done", `{"for *": allow}`, true},
		{"nested command still denied", "scripts/cmd $(forbidden)", `{"scripts/cmd *": allow, "$(*)": allow}`, true},
		{"backslash is not slash", `scripts\cmd arg`, `{"scripts/cmd *": allow}`, true},
		{"broad permission", "export FOO=bar; scripts/cmd arg > output &", `allow`, false},
		{"home expansion rule", "scripts/cmd $HOME", `{"scripts/cmd *": allow, "$HOME": allow}`, false},
		{"tilde expansion rule", "scripts/cmd ~/file", `{"scripts/cmd *": allow, "~/file": allow}`, false},
		{"literal heredoc", "scripts/cmd arg <<'EOF'\n* ? ~ {a,b}\nEOF", `{"scripts/cmd *": allow, "<<*": allow}`, false},
		{"case patterns are not globs", "case foo in *) scripts/cmd arg;; esac", `{"case *": allow, "scripts/cmd *": allow}`, false},
		{"escaped executable", `scripts/\* arg`, `{"scripts/\\* *": allow}`, false},
		{"bracket builtin is static", "[ -f input ]", `{"[ *": allow}`, false},
		{"empty brackets are literal", "scripts/[] arg", `{"scripts/[] *": allow}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			loop.Permissions = parsePermissionYAML(t, "bash: "+tt.rules)
			raw, err := json.Marshal(bashParams{Command: tt.command})
			require.NoError(t, err)
			decision, err := loop.permissionDecision("bash", &tool, raw)
			require.NoError(t, err)
			require.Equal(t, tt.denied, decision.denied, decision.message)
		})
	}

	loop.Permissions = parsePermissionYAML(t, "bash: allow")

	for _, command := range []string{
		"scripts/cmd $((FOO=1))", "((FOO=1)); scripts/cmd arg", "let FOO=1; scripts/cmd arg",
		"for FOO in bar; do scripts/cmd arg; done", "for ((i=0; i<2; i++)); do scripts/cmd arg; done",
		"if scripts/cmd arg; then scripts/cmd next; else scripts/cmd last; fi",
		"while scripts/cmd arg; do scripts/cmd next; done", "function f() { scripts/cmd arg; }; f",
		"scripts/cmd <(scripts/cmd arg)", "scripts/cmd ${FOO:=bar}", "[[ -f input ]]",
		"time scripts/cmd arg", "coproc scripts/cmd arg", "! scripts/cmd arg", "scripts/cmd arg |& scripts/cmd next",
	} {
		raw, err := json.Marshal(bashParams{Command: command})
		require.NoError(t, err)
		decision, err := loop.permissionDecision("bash", &tool, raw)
		require.NoError(t, err)
		require.False(t, decision.denied, "%s: %s", command, decision.message)
	}

	command := `export FOO=bar; printf '%s' "$FOO" > output`
	raw, err := json.Marshal(bashParams{Command: command})
	require.NoError(t, err)

	for _, grant := range []string{"export FOO=bar", command, `export FOO=*; printf '%s' "$FOO" > output`} {
		loop.Tools = tools
		loop.Permissions = parsePermissionYAML(t, `bash: {"printf *": allow, "$FOO": allow, ">output": allow}`)
		outputs, _, err := loop.dispatchToolCalls(t.Context(), responseWithFunctionCalls("response", []responses.ResponseFunctionToolCall{testFunctionCall("tool", "call", "bash", string(raw))}), nil, nil)
		require.NoError(t, err)
		require.Contains(t, outputs[0].Result.Output, "tool call denied")

		_, err = root.Stat("output")
		require.ErrorIs(t, err, os.ErrNotExist)

		if grant != "export FOO=bar" {
			loop.Permissions = PermissionSet{}
		}

		require.NoError(t, loop.Permissions.Allow("bash", grant))

		factory := &toolFactory{baseTools: tools}
		loop.Tools, loop.CodeModeHosts = factory.assembleTools(&Agent{Permission: loop.Permissions})
		executeArgs, err := json.Marshal(struct {
			Code string `json:"code"`
		}{Code: "def main():\n    return bash(command=r'''" + command + "''')"})
		require.NoError(t, err)
		_, _, err = loop.dispatchToolCalls(t.Context(), responseWithFunctionCalls("response", []responses.ResponseFunctionToolCall{testFunctionCall("tool", "call", executeToolName, string(executeArgs))}), nil, nil)
		require.NoError(t, err)
		contents, err := root.ReadFile("output")
		require.NoError(t, err)
		require.Equal(t, "bar", string(contents))
		require.NoError(t, root.Remove("output"))
	}
}

func TestFunctionToolStrictSchemasRequireAllProperties(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	outputDir := filepath.Join(dir, ".tmp", "shell-tmp")
	require.NoError(t, root.MkdirAll(filepath.Join(".tmp", "shell-tmp"), 0o755))

	tools := newSandboxedTools(root, testShellTempConfig(t, root, outputDir), nil, DefaultShellCommand)

	requireToolRequiredProperties(t, tools["glob"].Definition.Parameters, []string{"path", "pattern"})
	requireToolRequiredProperties(t, tools["grep"].Definition.Parameters, []string{"include", "path", "pattern"})
	requireToolRequiredProperties(t, tools["read"].Definition.Parameters, []string{"filePath", "offset"})
	requireToolRequiredProperties(t, tools["webfetch"].Definition.Parameters, []string{"format", "timeout_s", "url"})
	requireToolRequiredProperties(t, tools["bash"].Definition.Parameters, []string{"command", "description", "timeout_ms", "workdir"})
}

func requireToolRequiredProperties(t *testing.T, parameters map[string]any, required []string) {
	t.Helper()

	require.Equal(t, required, parameters["required"])
}
