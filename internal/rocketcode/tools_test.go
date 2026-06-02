package rocketcode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGlobGrepPermissionSubjectsMatchOpenCode(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	outputDir := filepath.Join(dir, ".tmp", "shell-outputs")
	require.NoError(t, root.MkdirAll(filepath.Join(".tmp", "shell-outputs"), 0o755))

	tools := newSandboxedTools(root, testShellOutputConfig(t, root, outputDir), nil, false)

	globSubjects, err := tools["glob"].Subjects(json.RawMessage(`{"pattern":"**/*.go","path":"src"}`))
	require.NoError(t, err)
	require.Equal(t, []string{"**/*.go"}, globSubjects)

	grepSubjects, err := tools["grep"].Subjects(json.RawMessage(`{"pattern":"func Test","path":"src","include":"*_test.go"}`))
	require.NoError(t, err)
	require.Equal(t, []string{"func Test"}, grepSubjects)
}

func TestWebFetchPermissionSubjectsMatchOpenCode(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	outputDir := filepath.Join(dir, ".tmp", "shell-outputs")
	require.NoError(t, root.MkdirAll(filepath.Join(".tmp", "shell-outputs"), 0o755))

	tools := newSandboxedTools(root, testShellOutputConfig(t, root, outputDir), nil, false)

	subjects, err := tools["webfetch"].Subjects(json.RawMessage(`{"url":"https://docs.example/path?q=1","format":"markdown"}`))

	require.NoError(t, err)
	require.Equal(t, []string{"https://docs.example/path?q=1"}, subjects)
}

func TestWebSearchPermissionIsCoarse(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	outputDir := filepath.Join(dir, ".tmp", "shell-outputs")
	require.NoError(t, root.MkdirAll(filepath.Join(".tmp", "shell-outputs"), 0o755))

	tools := newSandboxedTools(root, testShellOutputConfig(t, root, outputDir), nil, false)

	subjects, err := tools["websearch"].Subjects(json.RawMessage(`{}`))

	require.NoError(t, err)
	require.Equal(t, []string{"*"}, subjects)
	require.Equal(t, "web_search", *tools["websearch"].Hosted.GetType())
}

func TestFunctionToolStrictSchemasRequireAllProperties(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	outputDir := filepath.Join(dir, ".tmp", "shell-outputs")
	require.NoError(t, root.MkdirAll(filepath.Join(".tmp", "shell-outputs"), 0o755))

	tools := newSandboxedTools(root, testShellOutputConfig(t, root, outputDir), nil, false)

	requireToolRequiredProperties(t, tools["glob"].Definition.Parameters, []string{"path", "pattern"})
	requireToolRequiredProperties(t, tools["grep"].Definition.Parameters, []string{"include", "path", "pattern"})
	requireToolRequiredProperties(t, tools["read"].Definition.Parameters, []string{"filePath", "offset"})
	requireToolRequiredProperties(t, tools["webfetch"].Definition.Parameters, []string{"format", "timeout", "url"})
	requireToolRequiredProperties(t, tools["bash"].Definition.Parameters, []string{"command", "description", "timeout", "workdir"})
}

func requireToolRequiredProperties(t *testing.T, parameters map[string]any, required []string) {
	t.Helper()

	require.Equal(t, required, parameters["required"])
}
