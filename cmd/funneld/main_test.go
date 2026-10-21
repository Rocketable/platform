package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMainHelp(t *testing.T) {
	output, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
	require.NoError(t, err)

	args, stdout := os.Args, os.Stdout
	os.Args, os.Stdout = []string{"/opt/funneld", "--help"}, output
	t.Cleanup(func() {
		os.Args, os.Stdout = args, stdout
		require.NoError(t, output.Close())
	})
	t.Setenv("FUNNELD_CONFIG", filepath.Join(t.TempDir(), "missing.json"))

	main()

	help, err := os.ReadFile(output.Name())
	require.NoError(t, err)
	require.Contains(t, string(help), "Usage:\n\n  /opt/funneld [--config ./funneld.json]")
	require.Contains(t, string(help), "--http-addr  HTTP ACME challenge address override. Use \"none\" to disable.")
}

func TestMainError(t *testing.T) {
	if os.Getenv("FUNNELD_TEST_MAIN") == "1" {
		os.Args = []string{"funneld", "--unknown"}
		main()
		return
	}

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestMainError$")
	cmd.Env = append(os.Environ(), "FUNNELD_TEST_MAIN=1")
	output, err := cmd.Output()
	errExit, ok := errors.AsType[*exec.ExitError](err)
	require.True(t, ok, "expected an exit error, got %v", err)
	require.Equal(t, 1, errExit.ExitCode())
	require.Equal(t, "flag provided but not defined: -unknown\n", string(errExit.Stderr))
	require.Empty(t, output)
}
