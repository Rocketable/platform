package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/require"
)

// The tool stops its conversation's jobs, its subagents', and those of hidden runs reporting there; no others.
func TestStopBackgroundJobTool(t *testing.T) {
	store := newTestSessionService(t)
	started, release := make(chan struct{}, 4), make(chan struct{})
	// The wake of subagent /a holds its note until the test ends, so the note stays listed.
	registry := newBackgroundRegistry(store, &backgroundNotesMock{noteReadyFunc: func(string) {}, continueSubagentFunc: func(context.Context, *backgroundJob, string) (string, error) {
		<-release
		return "", nil
	}}, testLogger())

	defer close(release)

	for i, job := range []struct{ conversationID, childKey, destination string }{{"main", "", ""}, {"main", "/a", ""}, {"cron:daily", "", "main"}, {"other", "", ""}} {
		turn := backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: job.conversationID, origin: &protocol.InboundMessage{SyncDestination: job.destination}}
		_, err := turn.Run(t.Context(), &rocketcode.BackgroundJob{ID: fmt.Sprintf("turn-1/call/%d", i), Kind: rocketcode.BackgroundKindExecute, ChildKey: job.childKey, Detached: true}, blockedWork(started, release))
		require.NoError(t, err)
		<-started
	}

	tool := stopBackgroundJobTool(registry, "main")
	stop := func(jobID string) (rocketcode.ToolResult, error) {
		return tool.Call(t.Context(), json.RawMessage(`{"job_id":"`+jobID+`"}`), nil)
	}

	for _, jobID := range []string{"turn-1/call/0", "turn-1/call/1", "turn-1/call/2"} {
		result, err := stop(jobID)
		require.NoError(t, err)
		require.Equal(t, "Stopped background job "+jobID+".", result.Output)

		row := testBackgroundRow(t, store, "main", jobID)
		require.Equal(t, backgroundStopped, row.status)
		require.Equal(t, string(errStoppedByAgent), row.result)

		result, err = stop(jobID)
		require.NoError(t, err)
		require.Equal(t, "Background job "+jobID+" had already finished.", result.Output)
	}

	_, err := stop("turn-1/call/3")
	require.EqualError(t, err, `background job "turn-1/call/3" is neither running nor waiting to report in this conversation`)
	require.Equal(t, backgroundRunning, testBackgroundRow(t, store, "other", "turn-1/call/3").status)
}

// Only an exact allow_background rule shows the tool and lets it run.
func TestStopBackgroundJobToolNeedsAllowBackground(t *testing.T) {
	for permission, visible := range map[string]bool{"{}": false, "{rocketclaw: allow}": false, "{rocketclaw: {'*': allow}}": false, "{rocketclaw: {allow_background: deny}}": false, "{rocketclaw: {allow_background: allow}}": true} {
		t.Run(permission, func(t *testing.T) {
			workspace := t.TempDir()
			writeMainAgentSkills(t, workspace, "---\ndescription: Main\nmodel: gpt-5.5\nmode: primary\npermission: "+permission+"\n---\nPrompt\n")

			cfg := &config.Config{Workspace: workspace}
			root, agents, skills, resolver, err := prepareRocketCode(cfg, "main", slog.New(slog.DiscardHandler), toolModePersistent)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, root.Close()) })
			require.NoError(t, root.MkdirAll("shell", 0o700))

			bridge := &Bridge{runtime: cfg, config: Config{ConversationID: "main", SessionService: newTestSessionService(t)}, log: slog.New(slog.DiscardHandler)}
			runtimeConfig := bridge.rocketcodeConfig(filepath.Join(workspace, "shell"), nil)
			runtime, err := rocketcode.NewWithModelResolver(resolver, &runtimeConfig, root, agents, skills, "main", io.Discard)
			require.NoError(t, err)

			host, hosted := runtime.CodeModeHosts[stopBackgroundJobToolName]
			_, offered := runtime.Tools[stopBackgroundJobToolName]

			require.Equal(t, visible, hosted)
			require.False(t, offered, "platform tools run only inside execute")

			if visible {
				schema, err := json.Marshal(host.Definition.Parameters)
				require.NoError(t, err)
				require.JSONEq(t, `{"type":"object","properties":{"job_id":{"type":"string","description":"ID of the Background Job to stop."}},"required":["job_id"],"additionalProperties":false}`, string(schema))
			}
		})
	}
}
