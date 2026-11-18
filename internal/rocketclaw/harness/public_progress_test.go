package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeModeCallPullRequests(t *testing.T) {
	t.Parallel()

	type discoveryCase struct {
		Name      string   `json:"name"`
		Command   string   `json:"command"`
		Output    string   `json:"output"`
		Created   []string `json:"created"`
		Mentioned []string `json:"mentioned"`
	}

	raw, err := os.ReadFile("testdata/pr-discovery-cases.json")
	require.NoError(t, err)

	cases := make([]discoveryCase, 0, 2)
	require.NoError(t, json.Unmarshal(raw, &cases))

	links := make([]string, 101)
	for i := range links {
		links[i] = fmt.Sprintf("https://github.com/o/r/pull/%d", i+1)
	}

	cases = append(cases,
		discoveryCase{
			Name:    "a create whose command is over 4 KiB",
			Command: "gh pr create --title Fix --body '" + strings.Repeat("A long description line.\n", 200) + "'",
			Output:  "Creating pull request\nhttps://github.com/o/r/pull/7\nWarning: 1 uncommitted change\n",
			Created: []string{"https://github.com/o/r/pull/7"},
		},
		discoveryCase{Name: "more than 100 distinct links", Command: "cat links.txt", Output: strings.Join(links, "\n"), Mentioned: links[:100]},
	)

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()

			arguments, err := json.Marshal(bashParams{Command: c.Command})
			require.NoError(t, err)

			record := codeModeCallRecord(t.Context(), "bash", arguments)
			record.finish(ToolResult{Output: c.Output, Data: BashResult{Output: c.Output, Success: true}}, nil)
			require.Equal(t, codeModeCallOK, record.Status)
			require.Equal(t, [][]string{c.Created, c.Mentioned}, [][]string{record.CreatedPullRequests, record.MentionedPullRequests})
		})
	}
}

func TestCodeModeCallPullRequestsNeedSuccessfulBash(t *testing.T) {
	t.Parallel()

	const link = "https://github.com/o/r/pull/7\n"

	for _, tc := range []struct {
		name   string
		tool   string
		result ToolResult
		err    error
	}{
		{name: "failed", tool: "bash", result: ToolResult{Output: link, Data: BashResult{Output: link, ErrorCode: "1"}}},
		{name: "denied", tool: "bash", err: permissionDeniedError(link)},
		{name: "binary output", tool: "bash", result: ToolResult{Output: link + "\x00", Data: BashResult{Output: link + "\x00", Success: true}}},
		{name: "not bash", tool: "read", result: ToolResult{Output: link}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			record := codeModeCallRecord(t.Context(), tc.tool, json.RawMessage(`{"command":"gh pr create --fill"}`))
			record.finish(tc.result, tc.err)
			require.Empty(t, record.CreatedPullRequests)
			require.Empty(t, record.MentionedPullRequests)
		})
	}
}
