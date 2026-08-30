package developmentmcp

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProductionGoDoesNotImportEngine(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	fset := token.NewFileSet()

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, errParse := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		require.NoError(t, errParse)

		for _, spec := range file.Imports {
			path, errUnquote := strconv.Unquote(spec.Path.Value)
			require.NoError(t, errUnquote)
			assert.NotContains(t, path, "slack")
			assert.NotContains(t, path, "externalmcp")
			assert.NotContains(t, path, "/harnessbridge")
			assert.NotContains(t, path, "/cronjob")
			assert.NotContains(t, path, "/skel")
			assert.NotContains(t, path, "/agentlint")
		}
	}
}
