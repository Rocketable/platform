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
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	cronfrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// TestAgentEditorRealBackendBrowser runs the agent editor's browser test against a real backend: alice's
// browser at one address, and bob's, limited to Ops until /test/unlimit, at another.
func TestAgentEditorRealBackendBrowser(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	// The work provider, unknown to models.dev, serves only llama-4-maverick.
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

	// The openai provider answers each test turn "<model> answer <n>", n counting the user messages it was sent.
	// The agent editor's assistant runs on gpt-editor: it fills a described billing agent, gives the variant of an
	// answer pointed at new instructions and quotes that answer, and answers any other request with a shared chat.
	// A turn on gpt-held answers only after /test/release, so the Web test can keep a message waiting its turn.
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

	// The overlay github.com/acme/team reaches, through Git's insteadOf, a local repository whose trunk holds
	// router.md; gh, first on PATH and logged in, opens pull request #7 there and reports the state the file
	// pr-state holds, open until the Web test writes another.
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
		Providers: map[string]config.OpenAIConfig{"work": {APIKey: "key", APIBaseURL: work.URL, Auth: "api_key"}}}
	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.Mkdir("agents", 0o700))
	require.NoError(t, root.WriteFile("agents/support-bot.md", []byte("---\ndescription: Answers support questions\nmodel: gpt-6.1-sol\npermission: {}\n---\nYou answer support questions.\n"), 0o600))
	require.NoError(t, root.WriteFile("agents/helpdesk.md", []byte("---\ndescription: Answers help desk tickets\nmodel: gpt-help\npermission: {}\nsticky: user\n---\nLook up $ROCKETCLAW_METADATA_CUSTOMER_ID before answering.\n"), 0o600))
	require.NoError(t, root.WriteFile("agents/router.md", []byte("---\n# Routes every request.\ndescription: Routes requests\nmodel: gpt-route\npermission: {}\n---\nRoute each request.\n"), 0o600))

	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan *backend.Runtime)
	assembly := &mockFrontendAssembler{AssembleFunc: func(rt *backend.Runtime) (<-chan struct{}, []func(context.Context) error, error) {
		ready <- rt
		return rt.RunCtx.Done(), nil, nil
	}, ValidateAssetsFunc: func(c *config.Config, stage string) error {
		// Checking a stage that holds an agent named hold takes three seconds, so a test acts while its save runs.
		if _, err := os.Stat(filepath.Join(c.Workspace, stage, "agents", "hold.md")); err == nil {
			time.Sleep(3 * time.Second)
		}

		return nil
	}}

	var running errgroup.Group
	running.Go(func() error {
		return backend.Run(ctx, cfg, "", slog.New(slog.DiscardHandler), assembly, sandbox.None{})
	})
	t.Cleanup(func() { cancel(); require.NoError(t, running.Wait()) })

	rt := <-ready
	listener, err := net.Listen("unix", testSocketPath(t))
	require.NoError(t, err)

	transport := grpc.NewServer()
	New(rt, rt.Sessions, rt.Cfg, &mockChannels{}, &mockCronJobs{JobsFunc: func() ([]cronfrontend.Job, error) { return nil, nil }}).Register(transport)

	var serving errgroup.Group
	serving.Go(func() error { return transport.Serve(listener) })
	t.Cleanup(func() { transport.Stop(); require.NoError(t, serving.Wait()) })

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	handler := NewHTTPHandler(connection, cfg.Web.Sentry, cfg.PostHog, cfg.OpenCode, "", nil)
	alice := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/test/unlimit":
			unlimited := rt.Cfg.Clone()
			unlimited.Permissions = nil
			rt.Cfg.Store(unlimited)
		case "/test/release":
			close(held)
		default:
			handler.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(alice.Close)

	// The test tailscale names 192.0.2.2 bob.
	bob := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = "192.0.2.2:1234"
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(bob.Close)

	runWebTest(t, "src/agent-editor-transport.test.ts", "ROCKETCLAW_EDITOR_ALICE_URL="+alice.URL, "ROCKETCLAW_EDITOR_BOB_URL="+bob.URL, "ROCKETCLAW_EDITOR_WORKSPACE="+cfg.Workspace, "ROCKETCLAW_EDITOR_PR_STATE="+prState)
}
