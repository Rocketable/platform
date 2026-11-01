package backend

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

// An easy agent's workingDirectory starts from RocketClaw's working directory. Each turn variable becomes
// one path segment, escaped so different values never meet; RocketClaw's own variables are used as
// written. The folder is created, and refused where the agent could rewrite a definition or RocketClaw.
func TestResolveWorkingDirectory(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Chdir(workspace)

	outside, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("EASY_TEST_BASE", outside)

	cfg := &config.Config{Workspace: workspace}

	for _, tc := range []struct {
		dir, id, want, errWant string
	}{
		{"data/$ROCKETCLAW_CONVERSATION_ID", "slack-thread:C1:1.2", filepath.Join(workspace, "data", "slack-thread:C1:1.2"), ""},
		{"data/$ROCKETCLAW_CONVERSATION_ID", "slack-thread_C1_1.2", filepath.Join(workspace, "data", "slack-thread_C1_1.2"), ""},
		{"data/${ROCKETCLAW_METADATA_CUSTOMER_ID}", "c", filepath.Join(workspace, "data", "..%2F..%2Fx"), ""},
		{"data/$ROCKETCLAW_METADATA_DOTS/x", "c", filepath.Join(workspace, "data", "%..", "x"), ""},
		{"$EASY_TEST_BASE/x", "c", filepath.Join(outside, "x"), ""},
		{filepath.Join(outside, "abs"), "c", filepath.Join(outside, "abs"), ""},
		{"data/$NOPE", "c", "", "workingDirectory: $NOPE is not set"},
		{"skills/marketing", "c", "", "workingDirectory " + filepath.Join(workspace, "skills", "marketing") + " must not"},
		{"agents", "c", "", "must not"},
		{".", "c", "", "must not"},
		{"..", "c", "", "must not"},
		{".rocketclaw/x", "c", "", "must not"},
	} {
		got, err := resolveWorkingDirectory(cfg, tc.dir, map[string]string{rocketclawConversationIDEnv: tc.id, "ROCKETCLAW_METADATA_CUSTOMER_ID": "../../x", "ROCKETCLAW_METADATA_DOTS": ".."})
		if tc.errWant != "" {
			require.ErrorContains(t, err, tc.errWant, tc.dir)
			continue
		}

		require.NoError(t, err, tc.dir)
		require.Equal(t, tc.want, got, tc.dir)

		info, err := os.Stat(got)
		require.NoError(t, err)
		require.True(t, info.IsDir(), "the folder is created")
	}

	// A link may not lead the folder into a definitions folder.
	linked, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	root, err := os.OpenRoot(linked)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.Mkdir("agents", 0o755))
	require.NoError(t, root.Symlink("agents", "data"))

	_, err = resolveWorkingDirectory(&config.Config{Workspace: linked}, filepath.Join(linked, "data", "x"), nil)
	require.ErrorContains(t, err, "workingDirectory "+filepath.Join(linked, "agents", "x")+" must not")

	// Nor may it be, hold or lie inside where a definitions folder that is a link leads, out of the workspace.
	shared, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(shared, "agents"), 0o755))
	require.NoError(t, root.Symlink(filepath.Join(shared, "agents"), "skills"))

	for _, dir := range []string{shared, filepath.Join(shared, "agents"), filepath.Join(shared, "agents", "x")} {
		_, err = resolveWorkingDirectory(&config.Config{Workspace: linked}, dir, nil)
		require.ErrorContains(t, err, "workingDirectory "+dir+" must not", dir)
	}

	// A link to where no folder is yet fails, since the agent could make that folder.
	require.NoError(t, root.Symlink(filepath.Join(shared, "cron"), "cron"))

	_, err = resolveWorkingDirectory(&config.Config{Workspace: linked}, t.TempDir(), nil)
	require.ErrorContains(t, err, "workingDirectory: lstat "+filepath.Join(shared, "cron"))
}

// An easy agent's shell for a turn works in the folder of its top-level session, whatever its own
// conversation ID, and shows that session's uploads and the folders of its skills read-only, one inside
// another once. The emulator sees the turn's variables only; secrets reach host commands only.
func TestEasyTurnShell(t *testing.T) {
	t.Parallel()

	workspace, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	cfg := &config.Config{Workspace: workspace}
	skillsRoot := filepath.Join(workspace, ".rocketclaw", "skills")
	skills := harness.Skills{Root: skillsRoot, Items: map[string]harness.Skill{"a": {Name: "a", Location: "a/SKILL.md"}, "b": {Name: "b", Location: "a/b/SKILL.md"}, "c": {Name: "c", Location: "c/SKILL.md"}}}

	var permission harness.PermissionSet
	require.NoError(t, permission.Allow("skill", "a"))
	require.NoError(t, permission.Allow("skill", "b"))

	easy := harness.Agent{Mode: harness.AgentModeEasy, Permission: permission, CustomShell: harness.CustomShell{Emulated: true}, WorkingDirectory: filepath.Join(workspace, "data", "$ROCKETCLAW_CONVERSATION_ID"), Commands: map[string]string{"show": "printenv SECRET_KEY"}, SecretsARN: []string{"arn:a"}}
	advanced := harness.Agent{CustomShell: harness.CustomShell{Path: "/bin/sandbox"}}
	agents := harness.Agents{Items: map[string]harness.Agent{"easy": easy, "advanced": advanced}}
	turnEnv := map[string]string{rocketclawConversationIDEnv: "web:parent/sub", "ROCKETCLAW_PRINCIPAL_NAME": "Alice"}

	fetcher := &secretFetcherMock{SecretStringFunc: func(context.Context, string) (string, error) { return `{"SECRET_KEY":"s3cr3t"}`, nil }}
	b := &Bridge{config: Config{Secrets: fetcher}}

	require.NoError(t, b.setEasyShell(t.Context(), cfg, agents, skills, "easy", turnEnv, "web:parent"))
	require.Equal(t, t.Context(), fetcher.SecretStringCalls()[0].Ctx, "the turn's context stops the fetch")

	shell := agents.Items["easy"].CustomShell
	require.True(t, shell.Emulated)
	require.Equal(t, filepath.Join(workspace, "data", "web:parent"), shell.Dir, "a subagent works in its top-level session's folder")

	uploads := filepath.Join(workspace, ".rocketclaw", "uploads", "web:parent")
	require.Equal(t, []string{filepath.Join(skillsRoot, "a"), uploads}, shell.ReadOnly, "skill b's folder shows inside skill a's")
	require.DirExists(t, uploads, "the uploads folder exists to be shown")

	require.Equal(t, easy.Commands, shell.Commands)
	require.Equal(t, map[string]string{"SECRET_KEY": "s3cr3t"}, shell.Secrets, "secrets reach host commands and host-bash prompt expansion only")
	require.Equal(t, "web:parent/sub", turnEnv[rocketclawConversationIDEnv], "the turn's own variables are unchanged")

	require.NoError(t, b.setEasyShell(t.Context(), cfg, agents, skills, "advanced", turnEnv, "web:parent"))
	require.Equal(t, advanced, agents.Items["advanced"], "other agents keep their loaded shell")

	fetcher.SecretStringFunc = func(context.Context, string) (string, error) { return "", errors.New("denied") }
	agents.Items["easy"] = easy
	require.ErrorContains(t, b.setEasyShell(t.Context(), cfg, agents, skills, "easy", turnEnv, "web:parent"), "secretsARN: read secret string: denied")
	require.Equal(t, easy, agents.Items["easy"], "a failed setup leaves the shell failing closed")
}

// An easy shadow's file view and shell show the uploads of the chat it shadows, read-only.
func TestEasyShadowReadsParentUploads(t *testing.T) {
	t.Parallel()

	store, root := newShadowTestStore(t)
	require.NoError(t, (&Runtime{Sessions: store}).CreateConversation(t.Context(), protocol.Conversation{ID: "parent", Agent: "main"}))

	var child string
	require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT conversation_id FROM conversations WHERE shadow_parent = 'parent' LIMIT 1`).Scan(&child))

	upload := filepath.Join(".rocketclaw", "uploads", "parent", "up1", "report.txt")
	require.NoError(t, root.MkdirAll(filepath.Dir(upload), 0o700))
	require.NoError(t, root.WriteFile(upload, []byte("parent upload"), 0o600))

	folder, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	agents := harness.Agents{Items: map[string]harness.Agent{"easy": {Mode: harness.AgentModeEasy, CustomShell: harness.CustomShell{Emulated: true}, WorkingDirectory: folder}}}
	b := &Bridge{shadow: true, config: Config{SessionService: store, Secrets: &secretFetcherMock{}}}
	require.NoError(t, b.setEasyShell(t.Context(), &config.Config{Workspace: root.Name()}, agents, harness.Skills{}, "easy", map[string]string{rocketclawConversationIDEnv: child}, child))

	shell := agents.Items["easy"].CustomShell
	require.Equal(t, []string{filepath.Join(root.Name(), ".rocketclaw", "uploads", "parent")}, shell.ReadOnly)

	uploads, name, err := harness.EasyRoot(&shell, filepath.Join(root.Name(), upload))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, uploads.Close()) })

	data, err := uploads.ReadFile(name)
	require.NoError(t, err)
	require.Equal(t, "parent upload", string(data))
}

// A background subagent's turn, also when a restart resumes it, works in its top-level session's folder,
// though its own conversation ID differs, and runs on the model and with the tools its task call chose. One
// level down, with maxRecursion omitted, it delegates no further.
func TestEasySubagentWorksInParentFolder(t *testing.T) {
	t.Parallel()

	cut, started := make(chan struct{}, 1), make(chan struct{})
	cut <- struct{}{}

	subagentBodies := make(chan string, 2)
	nt := newNoteTest(t, func(w http.ResponseWriter, r *http.Request, n int, body string) {
		switch {
		case strings.Contains(body, "You are judging one planned coding-agent action."):
			writeRawRunMessage(t, w, "review", "verdict", `{"risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"ok"}`)
		case strings.Contains(body, "research the flaky test") && !strings.Contains(body, "function_call_output"):
			subagentBodies <- body

			select {
			case <-cut:
				close(started)
				<-r.Context().Done() // The restart cuts the subagent's first run.

				return
			default:
			}

			writeRawRunMessage(t, w, "resp_2", "msg_2", "found it")
		case !strings.Contains(body, "function_call_output"):
			writeRawRunFunctionCall(t, w, "resp_1", "task", map[string]any{"description": "research", "prompt": "research the flaky test", "model": "gpt-6-luna", "tools": []string{"bash", "task"}, "background": true})
		default:
			answerNoteTest(w, r, n, body)
		}
	})
	writeAgent(t, nt.cfg.Workspace, "main", "---\nmode: easy\nmodel: gpt-6.1-sol\nsubagents: [gpt-6-luna]\nspace: Ops\nworkingDirectory: "+filepath.Join(nt.cfg.Workspace, "data", "$ROCKETCLAW_CONVERSATION_ID")+"\n---\nPrompt\n")
	nt.cfg.AutoApproverModel = "gpt-review"

	data := filepath.Join(nt.cfg.Workspace, "data")
	folders := func() []string {
		entries, err := os.ReadDir(data)
		require.NoError(t, err)

		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}

		return names
	}

	first, shutdown := nt.run(t, ignoreOutbound)
	registry := nt.bridge(t, first, nt.conversationID).background
	require.NoError(t, nt.bridge(t, first, nt.conversationID).Submit(t.Context(), nt.slackPrompt("hello")))

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the subagent never ran")
	}

	assert.Equal(t, "answer", readFinal(t, nt.finals).Text)

	parent := nt.bridge(t, first, nt.conversationID)
	require.Eventually(t, func() bool { return !parent.handlingSnapshot() && len(parent.requestCh) == 0 }, 10*time.Second, 10*time.Millisecond)
	shutdown()
	registry.shutdown()
	require.Equal(t, []string{nt.conversationID}, folders())

	// The resolver makes folders on the host, outside any root; start again without them.
	require.NoError(t, os.RemoveAll(data))

	var model, tools string
	require.NoError(t, nt.service.db.QueryRowContext(t.Context(), `SELECT subagent_model, subagent_tools::text FROM conversations WHERE parent_conversation_id = $1`, nt.conversationID).Scan(&model, &tools))
	require.Equal(t, []string{"gpt-6-luna", `["bash", "task"]`}, []string{model, tools})

	manager, _ := nt.restart(t)
	readFinal(t, nt.finals)
	nt.waitIdle(t, manager, nt.conversationID)
	require.Equal(t, []string{nt.conversationID}, folders(), "the resumed subagent resolves to its parent's folder")

	for range 2 { // The cut run and the resumed one.
		body := <-subagentBodies
		assert.Contains(t, body, `"model":"gpt-6-luna"`)
		assert.Contains(t, body, `\n- bash(`)
		assert.NotContains(t, body, `\n- read(`, "execute offers only the chosen tools")
		assert.NotContains(t, body, `"name":"task"`, "the subagent is at its agent's recursion limit")
	}
}
