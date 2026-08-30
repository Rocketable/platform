package protocol

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEventsDoesNotImportEngine(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") {
			continue
		}

		data, errRead := os.ReadFile(name)
		require.NoError(t, errRead)

		for line := range strings.SplitSeq(string(data), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, `"`) && !strings.HasPrefix(line, `import "`) {
				continue
			}

			for _, forbidden := range []string{"/workflow", "/harnessbridge", "/rocketclaw/app", "/rocketclaw/backend"} {
				if strings.Contains(line, forbidden) {
					t.Errorf("%s imports %s", name, line)
				}
			}
		}
	}
}
