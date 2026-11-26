package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	routinefrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/routine"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const agentEditorEditingAndSaving = "the.agent.editor.starts|two.people.co-edit|saving.picks"

func TestAgentEditorEditingAndSavingRealBackendBrowser(t *testing.T) {
	runAgentEditorBrowser(t, "^(?:"+agentEditorEditingAndSaving+")")
}

func TestAgentEditorPreviewAndChatRealBackendBrowser(t *testing.T) {
	runAgentEditorBrowser(t, "^(?!(?:"+agentEditorEditingAndSaving+"))")
}

func runAgentEditorBrowser(t *testing.T, pattern string) {
	t.Helper()

	if os.Getenv("ROCKETCLAW_TEST_AGENT_EDITOR_CHILD") == "" {
		t.Parallel()

		executable, err := os.Executable()
		require.NoError(t, err)

		args := []string{"-test.run=^" + t.Name() + "$", "-test.cpu=" + strconv.Itoa(runtime.GOMAXPROCS(0)), "-test.v"}
		if testing.CoverMode() != "" {
			args = append(args, "-test.gocoverdir="+os.Getenv("GOCOVERDIR"))
		}

		cmd := exec.CommandContext(t.Context(), executable, args...)

		cmd.Env = append(os.Environ(), "ROCKETCLAW_TEST_AGENT_EDITOR_CHILD=1")
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
		t.Log(string(output))

		return
	}

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	work := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		w.Header().Set("Content-Type", "application/json")

		if body.Model != "llama-4-maverick" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"This gateway serves no such model; it serves llama-4-maverick","type":"invalid_request_error","code":"model_not_found"}}`)

			return
		}

		_, _ = io.WriteString(w, `{"id":"r","object":"response","created_at":0,"status":"completed","model":"llama-4-maverick","output":[]}`)
	}))
	t.Cleanup(work.Close)

	held := make(chan struct{})
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if !assert.NoError(t, err) {
			return
		}
		defer func() { _ = conn.Close() }()

		var request struct {
			Model string `json:"model"`
			Input []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"input"`
		}
		if !assert.NoError(t, conn.ReadJSON(&request)) {
			return
		}

		users, prompt := 0, ""

		for _, item := range request.Input {
			if item.Role == "user" {
				users++
				prompt = string(item.Content)
			}
		}

		if request.Model == "gpt-held" {
			select {
			case <-held:
			case <-t.Context().Done():
			}
		}

		text := fmt.Sprintf("%s answer %d", request.Model, users)

		if request.Model == "gpt-editor" {
			assert.NoError(t, json.Unmarshal([]byte(prompt), &prompt))

			text = `{"message":"Each person gets a chat of their own.","settings":[{"variant":"a","key":"sticky","value_yaml":"user","reason":"Every person keeps their own chat."}],"bodies":[]}`
			if strings.Contains(prompt, "# Request\nAn agent that answers billing questions") {
				text = `{"message":"A billing agent for the Billing space.","settings":[{"variant":"a","key":"space","value_yaml":"Billing","reason":"It serves the billing team."},{"variant":"a","key":"rocketclaw","value_yaml":"[sessions]","reason":"It needs only its chats."}],"bodies":[{"variant":"a","body":"You answer billing questions.\n","reason":"From your description."}]}`
			}

			if _, answer, ok := strings.Cut(prompt, "# Answer to fix, from variant \"b\"\n"); ok {
				answer, _, _ = strings.Cut(answer, "\n")
				text = fmt.Sprintf(`{"message":"It said %s.","settings":[],"bodies":[{"variant":"b","body":"Greet first.\n","reason":"The answer skipped the greeting."}]}`, answer)
			}
		}

		assert.NoError(t, conn.WriteMessage(websocket.TextMessage, fmt.Appendf(nil, `{"type":"response.completed","response":{"id":"resp","object":"response","created_at":0,"status":"completed","model":%q,"output":[{"id":"reply","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":%q,"annotations":[]}]}]}}`, request.Model, text)))
	}))
	t.Cleanup(openai.Close)

	team := t.TempDir()
	git := func(dir string, args ...string) {
		cmd := exec.Command("git", append([]string{"-c", "user.name=teammate", "-c", "user.email=teammate"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	git(team, "init", "--bare", "-b", "trunk", "team.git")
	git(team, "init", "-b", "trunk", "seed")
	require.NoError(t, os.Mkdir(filepath.Join(team, "seed", "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(team, "seed", "agents", "router.md"), []byte("---\ndescription: Routes requests\nmodel: gpt-route\npermission: {}\n---\nRoute each request.\n"), 0o600))
	git(filepath.Join(team, "seed"), "add", "-A")
	git(filepath.Join(team, "seed"), "commit", "-m", "seed")
	git(filepath.Join(team, "seed"), "push", filepath.Join(team, "team.git"), "trunk")
	require.NoError(t, os.Mkdir(filepath.Join(team, "bin"), 0o755))
	prState := filepath.Join(team, "pr-state")
	require.NoError(t, os.WriteFile(prState, []byte("OPEN\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(team, "bin", "gh"), []byte("#!/bin/sh\ncase \"$1 $2\" in\n\"pr create\") echo https://github.com/acme/team/pull/7;;\n\"pr view\") cat "+prState+";;\nesac\n"), 0o700))
	t.Setenv("PATH", filepath.Join(team, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+filepath.Join(team, "team.git")+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/acme/team")

	cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), Permissions: map[string][]string{"bob": {"Ops"}}, AgentEditorModel: "gpt-editor", Overlays: []string{"github.com/acme/team"},
		OpenAI:    config.OpenAIConfig{APIKey: "key", APIBaseURL: strings.Replace(openai.URL, "http://", "ws://", 1)},
		Web:       config.WebConfig{Tsnet: &config.TsnetConfig{Origin: "https://rocketclaw.example.test"}},
		Providers: map[string]config.OpenAIConfig{"work": {APIKey: "key", APIBaseURL: work.URL, Auth: "api_key"}}, Slack: liveSlack()}
	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.Mkdir("agents", 0o700))
	require.NoError(t, root.WriteFile("agents/support-bot.md", []byte("---\ndescription: Answers support questions\nmodel: gpt-6.1-sol\npermission: {}\n---\nYou answer support questions.\n"), 0o600))
	require.NoError(t, root.WriteFile("agents/helpdesk.md", []byte("---\ndescription: Answers help desk tickets\nmodel: gpt-help\npermission: {}\nsticky: user\n---\nLook up $ROCKETCLAW_METADATA_CUSTOMER_ID before answering.\n"), 0o600))
	require.NoError(t, root.WriteFile("agents/router.md", []byte("---\n# Routes every request.\ndescription: Routes requests\nmodel: gpt-route\npermission: {}\n---\nRoute each request.\n"), 0o600))

	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan *backend.Runtime)
	releaseSave := make(chan struct{})
	assembly := &mockFrontendAssembler{GateFunc: passGate, AssembleFunc: func(rt *backend.Runtime) (<-chan struct{}, []func(context.Context) error, error) {
		ready <- rt
		return rt.RunCtx.Done(), nil, nil
	}}

	var running errgroup.Group
	running.Go(func() error {
		return backend.Run(ctx, cfg, slog.New(slog.DiscardHandler), new(slog.LevelVar), assembly, sandbox.None{})
	})
	t.Cleanup(func() { cancel(); require.NoError(t, running.Wait()) })

	rt := <-ready
	listener, err := net.Listen("unix", testSocketPath(t))
	require.NoError(t, err)

	core := &mockBackend{
		ReloadFunc: rt.Reload,
		ValidateAgentFileFunc: func(name string, data []byte) error {
			if name == "hold" {
				select {
				case <-releaseSave:
				case <-t.Context().Done():
				}
			}

			return rt.ValidateAgentFile(name, data)
		},
		SaveBehaviorFunc:            rt.SaveBehavior,
		SubscribeFunc:               rt.Subscribe,
		CreateConversationFunc:      rt.CreateConversation,
		ContinueRunFunc:             rt.ContinueRun,
		SwitchConversationAgentFunc: rt.SwitchConversationAgent,
		RunningAgentFunc:            rt.RunningAgent,
		RunTurnFunc:                 rt.RunTurn,
		StageRevertFunc:             rt.StageRevert,
		DeleteBranchFunc:            rt.DeleteBranch,
		AgentPausedFunc:             rt.AgentPaused,
		SetAgentPausedFunc:          rt.SetAgentPaused,
		PostPausedMessageFunc:       rt.PostPausedMessage,
		QueueItemsFunc:              rt.QueueItems,
		PromoteQueueItemFunc:        rt.PromoteQueueItem,
		PopQueueItemFunc:            rt.PopQueueItem,
		DeleteQueueItemFunc:         rt.DeleteQueueItem,
		EditQueueItemFunc:           rt.EditQueueItem,
		ReorderQueueItemsFunc:       rt.ReorderQueueItems,
		StashQueueItemFunc:          rt.StashQueueItem,
		StartGoalFunc:               rt.StartGoal,
		MoveToBackgroundFunc:        rt.MoveToBackground,
		StopBackgroundJobFunc:       rt.StopBackgroundJob,
		BackgroundJobsFunc:          rt.BackgroundJobs,
		CompletionNotesFunc:         rt.CompletionNotes,
		PendingQuestionsFunc:        rt.PendingQuestions,
		AnswerQuestionFunc:          rt.AnswerQuestion,
		SandboxErrorFunc:            rt.SandboxError,
		SlackWarningFunc:            rt.SlackWarning,
	}

	transport := grpc.NewServer()
	New(core, rt.Sessions, rt.Cfg, &mockChannels{}, &mockRoutines{JobsFunc: func() ([]routinefrontend.Job, error) { return nil, nil }}).Register(transport)

	var serving errgroup.Group
	serving.Go(func() error { return transport.Serve(listener) })
	t.Cleanup(func() { transport.Stop(); require.NoError(t, serving.Wait()) })

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	web := NewHTTPHandler(connection, rt.Cfg, "")
	handler := signedIn("alice", web)
	alice := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/test/unlimit":
			unlimited := rt.Cfg.Clone()
			unlimited.Permissions = nil
			rt.Cfg.Store(unlimited)
		case "/test/release":
			close(held)
		case "/test/release-save":
			close(releaseSave)
		default:
			handler.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(alice.Close)

	bob := httptest.NewServer(signedIn("bob", web))
	t.Cleanup(bob.Close)

	runWebTest(t, "src/agent-editor-transport.test.ts", "BUN_OPTIONS=--test-name-pattern="+pattern, "ROCKETCLAW_EDITOR_ALICE_URL="+alice.URL, "ROCKETCLAW_EDITOR_BOB_URL="+bob.URL, "ROCKETCLAW_EDITOR_WORKSPACE="+cfg.Workspace, "ROCKETCLAW_EDITOR_PR_STATE="+prState)
}
