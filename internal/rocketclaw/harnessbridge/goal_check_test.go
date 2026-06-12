package harnessbridge

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateGoalCheckScriptAcceptsSafeSimpleCommand(t *testing.T) {
	root, workspace := testGoalCheckWorkspace(t)

	defer func() { require.NoError(t, root.Close()) }()

	permission := rocketcode.PermissionSet{}
	require.NoError(t, permission.Allow("bash", "./scripts/check.sh --linter-mode"))

	check, err := validateGoalCheckScript(root, workspace, `./scripts/check.sh --linter-mode`, permission)
	require.NoError(t, err)
	assert.Equal(t, "./scripts/check.sh --linter-mode", check.command)
	assert.Equal(t, "./scripts/check.sh --linter-mode", check.subject)
}

func TestValidateGoalCheckScriptRejectsUnsafeShapes(t *testing.T) {
	root, workspace := testGoalCheckWorkspace(t)

	defer func() { require.NoError(t, root.Close()) }()

	permission := rocketcode.PermissionSet{}
	require.NoError(t, permission.Allow("bash", "*"))

	for _, script := range []string{
		`./scripts/check.sh && ./banana.sh`,
		`./scripts/check.sh ; ./banana.sh`,
		`./scripts/check.sh || ./banana.sh`,
		`./scripts/check.sh &`,
		`(./scripts/check.sh)`,
		`./scripts/check.sh | tee out`,
		`./scripts/check.sh > out`,
		`FOO=bar ./scripts/check.sh`,
		`./scripts/check.sh "$(./banana.sh)"`,
		"./scripts/check.sh `./banana.sh`",
		`./scripts/check.sh <(./banana.sh)`,
		`bash -c "./scripts/check.sh && ./banana.sh"`,
	} {
		t.Run(script, func(t *testing.T) {
			_, err := validateGoalCheckScript(root, workspace, script, permission)
			require.Error(t, err)
		})
	}
}

func TestValidateGoalCheckScriptRequiresWorkspaceExecutable(t *testing.T) {
	root, workspace := testGoalCheckWorkspace(t)

	defer func() { require.NoError(t, root.Close()) }()

	require.NoError(t, root.WriteFile("scripts/not-executable.sh", []byte("#!/bin/sh\n"), 0o644))

	permission := rocketcode.PermissionSet{}
	require.NoError(t, permission.Allow("bash", "*"))

	for _, script := range []string{
		`/bin/echo ok`,
		`../outside.sh`,
		`./scripts/not-executable.sh`,
	} {
		t.Run(script, func(t *testing.T) {
			_, err := validateGoalCheckScript(root, workspace, script, permission)
			require.Error(t, err)
		})
	}
}

func TestValidateGoalCheckScriptChecksFullBashPermissionSubject(t *testing.T) {
	root, workspace := testGoalCheckWorkspace(t)

	defer func() { require.NoError(t, root.Close()) }()

	permission := rocketcode.PermissionSet{}
	require.NoError(t, permission.Allow("bash", "./scripts/check.sh --safe"))

	_, err := validateGoalCheckScript(root, workspace, `./scripts/check.sh --safe`, permission)
	require.NoError(t, err)

	_, err = validateGoalCheckScript(root, workspace, `./scripts/check.sh --dangerous`, permission)
	require.Error(t, err)

	require.NoError(t, permission.Allow("bash", "*"))
	require.NoError(t, permission.Deny("bash", "./scripts/check.sh --dangerous"))

	_, err = validateGoalCheckScript(root, workspace, `./scripts/check.sh --dangerous`, permission)
	require.Error(t, err)
}

func testGoalCheckWorkspace(t *testing.T) (root *os.Root, workspace string) {
	t.Helper()

	workspace = t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	require.NoError(t, root.MkdirAll("scripts", 0o755))
	require.NoError(t, root.WriteFile("scripts/check.sh", []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(t, root.WriteFile("banana.sh", []byte("#!/bin/sh\nexit 0\n"), 0o755))
	// This fixture intentionally lives outside root to exercise workspace-escape rejection.
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(workspace), "outside.sh"), []byte("#!/bin/sh\n"), 0o755))

	return root, workspace
}
