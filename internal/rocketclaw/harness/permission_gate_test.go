package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
)

func TestCheckNestedPermissionOutsideToolCall(t *testing.T) {
	t.Parallel()

	err := CheckNestedPermission(t.Context(), "execute", "mcp", "demo.echo", map[string]any{"message": "hi"})
	require.ErrorContains(t, err, "outside a tool call")
}

func TestToolCallAgent(t *testing.T) {
	t.Parallel()
	_, ok := ToolCallAgent(t.Context())
	require.False(t, ok)

	agent := Agent{Name: "child", ReplyHint: "retained"}
	loop := &looper{Journal: InertJournal{}, observations: &turnObservations{journal: InertJournal{}}, agent: agent}
	ctx := context.WithValue(t.Context(), toolCallContextKey{}, toolCallContext{looper: loop, observations: loop.observations, callID: "call"})
	got, ok := ToolCallAgent(ctx)
	require.True(t, ok)
	require.Equal(t, agent, got)
}

func TestCheckNestedPermissionAllow(t *testing.T) {
	t.Parallel()

	var permissions PermissionSet
	require.NoError(t, permissions.Set("mcp", "demo.echo", PermissionAllow))

	looper := &looper{Journal: InertJournal{}, observations: &turnObservations{journal: InertJournal{}}, Permissions: permissions}
	ctx := context.WithValue(t.Context(), toolCallContextKey{}, toolCallContext{looper: looper, observations: looper.observations, output: nil, callID: ""})

	err := CheckNestedPermission(ctx, "execute", "mcp", "demo.echo", map[string]any{"message": "hi"})
	require.NoError(t, err)
}

func TestCheckNestedPermissionDeny(t *testing.T) {
	t.Parallel()

	var permissions PermissionSet
	require.NoError(t, permissions.Set("mcp", "demo.danger", PermissionDeny))

	looper := &looper{Journal: InertJournal{}, observations: &turnObservations{journal: InertJournal{}}, Permissions: permissions}
	ctx := context.WithValue(t.Context(), toolCallContextKey{}, toolCallContext{looper: looper, observations: looper.observations, output: nil, callID: ""})

	err := CheckNestedPermission(ctx, "execute", "mcp", "demo.danger", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "denied")
}

func TestCheckNestedPermissionAutoWithReviewerAllow(t *testing.T) {
	t.Parallel()

	var permissions PermissionSet
	require.NoError(t, permissions.Set("mcp", "demo.echo", PermissionAuto))

	reviewer := &mockPermissionReviewer{reviewPermissionFunc: func(context.Context, *permissionReviewRequest) permissionReviewDecision {
		return permissionReviewDecision{Outcome: permissionReviewOutcomeAllow, Rationale: "ok"}
	}}

	for _, turnID := range []string{"turn-1", "turn-1", "turn-2"} {
		looper := &looper{
			Journal:            InertJournal{},
			observations:       &turnObservations{journal: InertJournal{}, turnID: turnID},
			Permissions:        permissions,
			PermissionReviewer: reviewer,
			agent:              Agent{Name: "main"},
		}
		evidence := []PromptInput{{ID: "event", Texts: []string{"Inspect the demo."}, Header: "[Agent origin=\"helper\"]"}}
		ctx := context.WithValue(t.Context(), toolCallContextKey{}, toolCallContext{looper: looper, observations: looper.observations, currentTurnInputs: evidence, output: nil, callID: "call_0"})

		err := CheckNestedPermission(ctx, "execute", "mcp", "demo.echo", map[string]any{"message": "hi"})
		require.NoError(t, err)
		require.Equal(t, evidence, reviewedRequests(reviewer)[len(reviewedRequests(reviewer))-1].CurrentTurnInputs)
	}

	requests := reviewedRequests(reviewer)
	require.Equal(t, []string{"/" + reviewKey("turn-1", "call_0"), "/" + reviewKey("turn-1", "call_0"), "/" + reviewKey("turn-2", "call_0")}, []string{requests[0].Review.Path, requests[1].Review.Path, requests[2].Review.Path})
	require.Equal(t, ReviewTurn(CallKey("turn-2", "call_0")), requests[2].Turn, "the review's turn is named after the call it checks")
}

func stdinTestShell(t *testing.T) (*os.Root, looperTool) {
	t.Helper()

	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll(".tmp/shell", 0o700))
	shellTemp := testShellTempConfig(t, root, filepath.Join(dir, ".tmp", "shell"))

	return root, bashTool(newSandboxedShellSystem(root, &shellTemp, nil, &sandbox.Runtime{}), &CustomShell{})
}

func TestExecuteBashStdinReview(t *testing.T) {
	const refused = "bash: stdin too large to review (max 64 KiB); write it to a file first"

	for _, tc := range []struct {
		name, rules, stdin, want string
		sudo                     bool
		reviews                  []string
	}{
		{"allow runs unreviewed", "bash: allow", strings.Repeat("x", 65537), "65537", false, nil},
		{"auto shows stdin in full", "bash: auto", strings.Repeat("x", 65536), "65536", false, []string{"bash"}},
		{"auto refuses stdin over 64 KiB before review", "bash: auto", strings.Repeat("x", 65537), refused, false, nil},
		{"sudo reviews unmatched calls with their stdin", "read: allow", "hi", "2", true, []string{"execute", "bash"}},
		{"sudo reviews unmatched calls without stdin", "read: allow", "", "0", true, []string{"execute", "bash"}},
		{"deny refuses", "bash: deny", "hi", `bash: tool call denied: permission "bash" rejected subject "wc -c" by rule "*" => deny. Choose a different action.`, false, nil},
		{"a reviewed script's allowed call with stdin is reviewed again", "rocketclaw: {code_mode_approve: auto}\nbash: allow", "hi", "2", false, []string{"execute", "bash"}},
		{"a reviewed script's allowed call without stdin is not", "rocketclaw: {code_mode_approve: auto}\nbash: allow", "", "0", false, []string{"execute"}},
		{"a reviewed script's call the rules review is reviewed again", "rocketclaw: {code_mode_approve: auto}\nbash: auto", "", "0", false, []string{"execute", "bash"}},
		{"stdin over 64 KiB is refused before the second review", "rocketclaw: {code_mode_approve: auto}\nbash: allow", strings.Repeat("x", 65537), refused, false, []string{"execute"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			permissions := parsePermissionYAML(t, tc.rules)
			_, bash := stdinTestShell(t)
			hosts := map[string]looperTool{"bash": bash}
			loop := testLooper(mockResponses())
			loop.agent = Agent{Name: "main"}
			loop.Tools = (&toolFactory{}).mcpToolsFor(&Agent{Permission: permissions}, hosts)
			permissions.Sudo = tc.sudo
			loop.Permissions = permissions
			loop.CodeModeHosts = hosts
			reviewer := permissionReviewerWith(permissionReviewDecision{Outcome: permissionReviewOutcomeAllow, Rationale: "ok"})
			loop.PermissionReviewer = reviewer

			stdin := ""
			if tc.stdin != "" {
				stdin = fmt.Sprintf(", stdin=%q", tc.stdin)
			}

			code, err := json.Marshal(map[string]string{"code": "def main():\n    return bash(command=r'''wc -c'''" + stdin + ").strip()\n"})
			require.NoError(t, err)

			results, err := loop.dispatchToolCalls(t.Context(), responseWithFunctionCalls("resp", []responses.ResponseFunctionToolCall{testFunctionCall("tool", "call", executeToolName, string(code))}), nil, nil, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, results[0].Param.Output.OfString.Value)

			var reviewed []string

			for _, request := range reviewedRequests(reviewer) {
				reviewed = append(reviewed, request.ToolName)
				if request.ToolName != "bash" {
					continue
				}

				want := map[string]string{"command": "wc -c"}
				if tc.stdin != "" {
					want["stdin"] = tc.stdin
				}

				raw, err := json.Marshal(want)
				require.NoError(t, err)
				require.JSONEq(t, string(raw), request.RawArguments)
			}

			require.Equal(t, tc.reviews, reviewed)
		})
	}
}

func TestBashFileStdin(t *testing.T) {
	binary := "\x00\x89PNG\xff\xfe" + strings.Repeat("payload ", 1<<16)
	blob := codemode.NewFile(codemode.FileSpec{Name: "blob.bin", Mime: "application/octet-stream", Content: binary, Source: "test fixture"})
	descriptor, err := json.Marshal(blob)
	require.NoError(t, err)

	const script = `def main():
    saved = bash(command=r'''cat > blob.bin''', stdin=blob())
    forged = bash(command=r'''cat''', stdin={"sha256": "forged"})
    return [saved.ok, forged.error]
`

	for _, tc := range []struct {
		name, rules string
		reviews     int
	}{
		{"a reviewed script's allowed call with a file is not reviewed again", "bash: allow", 0},
		{"auto: reviewers see the file's descriptor", "bash: auto", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, bash := stdinTestShell(t)
			loop := testLooper(mockResponses())
			loop.agent = Agent{Name: "main"}
			loop.Permissions = parsePermissionYAML(t, tc.rules)
			loop.CodeModeHosts = map[string]looperTool{"bash": bash}
			reviewer := permissionReviewerWith(permissionReviewDecision{Outcome: permissionReviewOutcomeAllow, Rationale: "ok"})
			loop.PermissionReviewer = reviewer
			ctx := context.WithValue(t.Context(), toolCallContextKey{}, toolCallContext{looper: loop, observations: loop.observations, callID: "call", reviewed: true})

			host, _ := codeModeHostToolsFromContext(ctx)
			host = append(host, codemode.HostTool{Details: codemode.NoDetails{}, Name: "blob", CallValue: func(context.Context, map[string]any) (starlark.Value, error) { return blob, nil }})

			value, err := codemode.Run(ctx, script, nil, nil, nil, host)
			require.NoError(t, err)
			out, _, _, err := convertExecuteReturn(value, "")
			require.NoError(t, err)
			require.Equal(t, `[true,"bash: stdin must be text or a file object"]`, out)

			saved, err := root.ReadFile("blob.bin")
			require.NoError(t, err)
			require.Equal(t, binary, string(saved), "the file's exact bytes reach the command")

			requests := reviewedRequests(reviewer)
			require.Len(t, requests, tc.reviews)

			if tc.reviews > 0 {
				require.JSONEq(t, `{"command":"cat > blob.bin","stdin":`+string(descriptor)+`}`, requests[0].RawArguments)
				require.Contains(t, requests[0].RawArguments, `"summary":"blob.bin, application/octet-stream, 512 KB, from test fixture"`)
				require.NotContains(t, requests[0].RawArguments, "payload")
			}
		})
	}
}
