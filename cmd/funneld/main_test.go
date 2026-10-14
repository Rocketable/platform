package main

import (
	"os"
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
