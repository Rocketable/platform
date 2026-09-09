package rocketcode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

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
