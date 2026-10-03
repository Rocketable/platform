package rpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	cronfrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

func TestPublicProgressRealBrowser(t *testing.T) {
	finals := []string{"short", "empty", "stop", "calls"}
	env := make([]string, 0, len(finals))
	checks := make([]func(), 0, len(finals))

	for _, final := range finals {
		dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
		require.NoError(t, err)

		releaseProvider := make(chan struct{})

		release := sync.OnceFunc(func() { close(releaseProvider) })
		t.Cleanup(release)

		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if !assert.NoError(t, err) {
				return
			}
			defer func() { _ = conn.Close() }()

			_, _, err = conn.ReadMessage()
			if !assert.NoError(t, err) {
				return
			}

			for _, event := range []string{
				`{"type":"response.created","response":{"id":"resp"}}`,
				`{"type":"response.reasoning_text.delta","delta":"REASONING_PRIVATE_SENTINEL"}`,
				`{"type":"response.function_call_arguments.delta","item_id":"provisional","delta":"ARGUMENT_PRIVATE_SENTINEL"}`,
				`{"type":"response.output_text.delta","item_id":"msg","content_index":0,"delta":"Held public partial suffix"}`,
			} {
				if !assert.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(event))) {
					return
				}
			}

			<-releaseProvider

			if final == "stop" {
				return
			}

			text := "Short"
			if final == "empty" {
				text = ""
			}

			assert.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"`+text+`","annotations":[]}]}]}}`)))
		}))
		t.Cleanup(provider.Close)
		cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}, OpenAI: config.OpenAIConfig{APIKey: "owned-test", APIBaseURL: strings.Replace(provider.URL, "http://", "ws://", 1)}}
		root, err := os.OpenRoot(cfg.Workspace)

		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, root.Close()) })

		require.NoError(t, root.MkdirAll(filepath.Join(cfg.RuntimeDirName(), "agents"), 0o700))
		require.NoError(t, root.WriteFile(filepath.Join(cfg.RuntimeDirName(), "agents", "main.md"), []byte("---\ndescription: Owned browser fixture\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nReply plainly.\n"), 0o600))

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		ready := make(chan *backend.Runtime)
		assembly := &mockFrontendAssembler{
			AssembleFunc: func(rt *backend.Runtime) (backend.SlackFrontend, <-chan struct{}, []func(context.Context) error, error) {
				ready <- rt
				return nil, rt.RunCtx.Done(), nil, nil // The public assembler contract permits absent Slack.
			},
			ValidateAssetsFunc: func(*config.Config, string, []string) error { return nil },
		}

		var running errgroup.Group
		running.Go(func() error {
			return backend.Run(ctx, cfg, "", slog.New(slog.DiscardHandler), assembly)
		})

		t.Cleanup(func() {
			release()
			cancel()
			require.NoError(t, running.Wait())
		})

		rt := <-ready
		id := "owned-public-" + final
		require.NoError(t, rt.CreateConversation(ctx, protocol.Conversation{ID: id, Agent: "main", CreatedBy: "alice"}))
		checkpoint := &rocketcode.ActiveTurnCheckpoint{TurnID: "calls", ConversationKey: id, Agent: "main", DisplayModel: "root/model"}

		if final == "calls" {
			// Producer timing is covered by RocketCode's held-worker tests; these
			// committed checkpoints exercise real signal/fetch/browser reconciliation.
			for _, call := range []string{"A", "B", "C"} {
				checkpoint.ReplayInput = append(checkpoint.ReplayInput, json.RawMessage(`{"type":"function_call","call_id":"`+call+`","name":"task","arguments":"{}"}`))
				checkpoint.OutputTrace = append(checkpoint.OutputTrace, json.RawMessage(`{"type":"rocketcode_public_progress","progress":{"id":"`+call+`","parent_id":"calls/response","kind":"delegation","state":"working","agent":"child","model":"child/model"}}`))
			}

			require.NoError(t, rt.Sessions.UpsertActiveTurn(ctx, checkpoint, nil))
		}

		listener, err := Listen(testSocketPath(t))
		require.NoError(t, err)

		transport := grpc.NewServer()
		New(rt, rt.Sessions, cfg, &mockChannels{}, &mockCronJobs{JobsFunc: func() ([]cronfrontend.Job, error) {
			return nil, nil
		}}).Register(transport)

		var serving errgroup.Group
		serving.Go(func() error {
			if err := transport.Serve(listener); err != nil {
				return fmt.Errorf("serve public progress RPC: %w", err)
			}

			return nil
		})
		t.Cleanup(func() {
			transport.Stop()
			require.NoError(t, serving.Wait())
		})

		connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, connection.Close()) })
		bundle := filepath.Join(t.TempDir(), "current.js")
		build := exec.CommandContext(ctx, "bun", "build", "./src/main.tsx", "--target=browser", "--define", `process.env.NODE_ENV="production"`, "--outfile="+bundle)
		build.Dir = "../../web"
		output, err := build.CombinedOutput()
		require.NoError(t, err, "%s", output)
		html, err := os.ReadFile("../../internal/web/dist/index.html")
		require.NoError(t, err)

		html = regexp.MustCompile(`<script type="module" src="[^"]+"></script>`).ReplaceAll(html, []byte(`<script type="module" src="/current.js"></script>`))
		handler := NewHTTPHandler(connection)
		httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/current.js":
				http.ServeFile(w, r, bundle)
			case r.URL.Path == "/test/release":
				release()
			case strings.HasPrefix(r.URL.Path, "/test/calls/"):
				call := strings.TrimPrefix(r.URL.Path, "/test/calls/")

				progress := rocketcode.PublicProgressFromTrace(checkpoint.OutputTrace)
				for i := range progress {
					if progress[i].ID != call {
						continue
					}

					progress[i].State = rocketcode.PublicProgressCompleted
					if call == "C" {
						progress[i].State = rocketcode.PublicProgressBlocked
					}

					raw, err := json.Marshal(struct {
						Type     string                    `json:"type"`
						Progress rocketcode.PublicProgress `json:"progress"`
					}{"rocketcode_public_progress", progress[i]})
					assert.NoError(t, err)

					checkpoint.OutputTrace[i] = raw
				}

				if call == "A" {
					for _, output := range []string{"A", "B", "C"} {
						text := output + " result"
						if output == "C" {
							text = "REVIEWER_PRIVATE_SENTINEL"
						}

						checkpoint.ReplayInput = append(checkpoint.ReplayInput, json.RawMessage(`{"type":"function_call_output","call_id":"`+output+`","output":"`+text+`"}`))
					}

					_, err := rt.Sessions.AppendEntryID(ctx, id, &rocketcode.SessionEntry{Type: "turn", TurnID: checkpoint.TurnID, Agent: checkpoint.Agent, Model: checkpoint.DisplayModel, ReplayInput: checkpoint.ReplayInput, OutputTrace: checkpoint.OutputTrace})
					assert.NoError(t, err)
					assert.NoError(t, rt.Sessions.ClearActiveTurn(ctx, checkpoint.TurnID))
				} else {
					assert.NoError(t, rt.Sessions.UpsertActiveTurn(ctx, checkpoint, nil))
				}
			case r.URL.Path == "/" || strings.HasPrefix(r.URL.Path, "/s/"):
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write(html)
			default:
				handler.ServeHTTP(w, r)
			}
		}))
		t.Cleanup(httpServer.Close)

		env = append(env, "ROCKETCLAW_PUBLIC_TEST_URL_"+final+"="+httpServer.URL)
		checks = append(checks, func() {
			entries, err := rt.Sessions.ObserveTranscript(ctx, id, 0, 0, nil)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			require.False(t, entries[0].Active)

			if final == "stop" {
				require.Equal(t, protocol.TerminalStopped, entries[0].Terminal)
			}

			if final == "calls" {
				require.Equal(t, checkpoint.ReplayInput, entries[0].Entry.ReplayInput, "canonical outputs retain original A/B/C order and rejected content")
			}

			recoverable, err := rt.Sessions.RecoverableActiveTurns(ctx)
			require.NoError(t, err)
			require.Empty(t, recoverable)
		})
	}

	runWebTest(t, "src/public-progress-transport.test.ts", env...)

	for _, check := range checks {
		check()
	}
}

func TestHistoryReadsActiveReplay(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}
	sessions, err := backend.NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })
	require.NoError(t, sessions.UpsertThread("chat", backend.ThreadState{Agent: "main"}))
	checkpoint := &rocketcode.ActiveTurnCheckpoint{TurnID: "turn-1", ConversationKey: "chat", Agent: "main", Model: "model", ReplayInput: []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","input_id":"input-1","prompt_header":"[Web principal=\"alice\"]","content":"[Web principal=\"alice\"]\n\nquestion"}`),
		json.RawMessage(`{"type":"reasoning","summary":[{"text":"Recorded summary"}],"encrypted_content":"secret"}`),
		json.RawMessage(`{"type":"function_call","call_id":"call-1","name":"execute","arguments":"{\"code\":\"read()\"}"}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"call-1","output":"readable result"}`),
	}}
	require.NoError(t, sessions.UpsertActiveTurn(t.Context(), checkpoint, nil))
	server := &Server{sessions: sessions, cfg: cfg, usernames: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1"))
	view, err := server.history(ctx, &HistoryRequest{Id: "chat"})
	require.NoError(t, err)
	require.Len(t, view.Messages, 4)
	require.Equal(t, "input-1", view.Messages[0].InputId)
	require.Equal(t, "question", view.Messages[0].Text)
	require.Equal(t, "Recorded summary", view.Messages[1].Text)
	require.Equal(t, "call-1", view.Messages[2].ToolCallId)
	require.Equal(t, "readable result", view.Messages[3].Text)
	require.True(t, view.Reset_)
	require.True(t, view.Running)
	require.Equal(t, []string{"turn:chat:turn-1"}, view.EntryKeys)
	require.Equal(t, view.EntryKeys, view.ReplacedKeys)
	require.Empty(t, view.Messages[0].MessageId, "active inputs are not stored-command targets")
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret")

	unchanged, err := server.history(ctx, &HistoryRequest{Id: "chat", Revision: view.Revision})
	require.NoError(t, err)
	require.False(t, unchanged.Reset_)
	require.True(t, unchanged.Running)
	require.Empty(t, unchanged.Messages)
	require.Empty(t, unchanged.ReplacedKeys)
	require.Equal(t, view.Revision, unchanged.Revision)

	checkpoint.ReplayInput = append(checkpoint.ReplayInput, json.RawMessage(`{"type":"message","role":"assistant","content":"final answer"}`))
	require.NoError(t, sessions.UpsertActiveTurn(ctx, checkpoint, nil))
	changed, err := server.history(ctx, &HistoryRequest{Id: "chat", Revision: view.Revision})
	require.NoError(t, err)
	require.Equal(t, view.EntryKeys, changed.ReplacedKeys)
	require.Len(t, changed.Messages, 5)
	require.Equal(t, view.Messages[0].ItemId, changed.Messages[0].ItemId)

	entry := &rocketcode.SessionEntry{Type: "turn", TurnID: checkpoint.TurnID, Agent: checkpoint.Agent, Model: checkpoint.Model, ReplayInput: checkpoint.ReplayInput}
	_, err = sessions.AppendEntryID(ctx, "chat", entry)
	require.NoError(t, err)
	finished, err := server.history(ctx, &HistoryRequest{Id: "chat", Revision: changed.Revision})
	require.NoError(t, err)
	require.False(t, finished.Running, "saved turn supersedes the checkpoint before its clear")
	require.Len(t, finished.Messages, 5)
	require.Equal(t, changed.EntryKeys, finished.EntryKeys)
	require.Equal(t, changed.Messages[0].ItemId, finished.Messages[0].ItemId)
	require.NotEmpty(t, finished.Messages[0].MessageId, "stored commands retain database entry identity")
	require.NoError(t, sessions.ClearActiveTurn(ctx, checkpoint.TurnID))
	cleared, err := server.history(ctx, &HistoryRequest{Id: "chat", Revision: finished.Revision})
	require.NoError(t, err)
	require.Equal(t, finished.Revision, cleared.Revision)
	require.Empty(t, cleared.Messages)

	_, err = sessions.DeleteSession(ctx, "chat")
	require.NoError(t, err)
	removed, err := server.history(ctx, &HistoryRequest{Id: "chat", Revision: cleared.Revision})
	require.NoError(t, err)
	require.Equal(t, cleared.EntryKeys, removed.RemovedKeys)
	require.Empty(t, removed.EntryKeys)
	require.Empty(t, removed.Messages)

	checkpoint.TurnID = "failed-turn"
	require.NoError(t, sessions.UpsertActiveTurn(ctx, checkpoint, nil))
	require.NoError(t, sessions.SetActiveTurnTerminal(ctx, checkpoint.TurnID, "failed"))
	failed, err := server.history(ctx, &HistoryRequest{Id: "chat", Revision: removed.Revision})
	require.NoError(t, err)
	require.False(t, failed.Running)
	require.Equal(t, "failed", failed.Terminal)
	require.Len(t, failed.Messages, 5)

	_, err = sessions.DeleteSession(ctx, "chat")
	require.NoError(t, err)
	deletedFailure, err := server.history(ctx, &HistoryRequest{Id: "chat", Revision: failed.Revision})
	require.NoError(t, err)
	require.Equal(t, failed.EntryKeys, deletedFailure.RemovedKeys)
	require.Empty(t, deletedFailure.Messages)
	require.Empty(t, deletedFailure.Terminal)

	for _, revision := range []string{"invalid", view.Revision} {
		// A token from another source filter must never suppress the initial read.
		reset, err := server.history(ctx, &HistoryRequest{Id: "chat", SourceConversationId: "other", Revision: revision})
		require.NoError(t, err)
		require.True(t, reset.Reset_)
		require.Empty(t, reset.Messages)
	}
}

func TestHistoryFollowsNewestEntries(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}
	sessions, err := backend.NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })
	require.NoError(t, sessions.UpsertThread("chat", backend.ThreadState{Agent: "main"}))
	server := &Server{sessions: sessions, cfg: cfg, usernames: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1"))

	var keys []string

	appendTurn := func(text string, items ...json.RawMessage) int64 {
		id, err := sessions.AppendEntryID(ctx, "chat", &rocketcode.SessionEntry{Type: "turn", ReplayInput: append([]json.RawMessage{
			json.RawMessage(`{"type":"message","role":"user","content":"` + text + `"}`),
			json.RawMessage(`{"type":"message","role":"assistant","content":"reply"}`),
		}, items...)})
		require.NoError(t, err)

		keys = append(keys, strconv.FormatInt(id, 10))

		return id
	}
	ids := []int64{appendTurn("one", json.RawMessage(`{"type":"function_call","call_id":"early","name":"task","arguments":"{}"}`)), appendTurn("two"), appendTurn("three"), appendTurn("four")}
	_, err = sessions.AppendEntryID(ctx, "chat/early", &rocketcode.SessionEntry{Type: "turn"})
	require.NoError(t, err)

	tail, err := server.history(ctx, &HistoryRequest{Id: "chat", Limit: 2})
	require.NoError(t, err)
	require.True(t, tail.Reset_)
	require.Equal(t, keys[2:], tail.EntryKeys)
	require.Equal(t, "three", tail.Messages[0].Text)
	require.Equal(t, ids[2], tail.GetStart())
	require.True(t, tail.GetMore())
	require.Empty(t, tail.Delegations, "delegations come from the returned entries only")

	appendTurn("five")

	delta, err := server.history(ctx, &HistoryRequest{Id: "chat", Limit: 2, Revision: tail.Revision})
	require.NoError(t, err)
	require.False(t, delta.Reset_)
	require.Equal(t, keys[2:], delta.EntryKeys, "an open view keeps following the entries it already shows")
	require.Equal(t, keys[4:], delta.ReplacedKeys)
	require.Equal(t, ids[2], delta.GetStart())

	page, err := server.history(ctx, &HistoryRequest{Id: "chat", Limit: 2, Before: delta.GetStart()})
	require.NoError(t, err)
	require.Equal(t, keys[:2], page.EntryKeys)
	require.Equal(t, "one", page.Messages[0].Text)
	require.Equal(t, ids[0], page.GetStart())
	require.False(t, page.GetMore())
	require.Empty(t, page.Revision, "settled pages are not followed")
	require.Equal(t, []string{"chat/early"}, page.Delegations)

	jump, err := server.history(ctx, &HistoryRequest{Id: "chat", Before: delta.GetStart(), From: ids[1]})
	require.NoError(t, err)
	require.Equal(t, keys[1:2], jump.EntryKeys)
	require.Equal(t, ids[1], jump.GetStart())
	require.True(t, jump.GetMore())

	_, err = sessions.DeleteSession(ctx, "chat")
	require.NoError(t, err)
	appendTurn("again")

	restarted, err := server.history(ctx, &HistoryRequest{Id: "chat", Limit: 2, Revision: delta.Revision})
	require.NoError(t, err)
	require.True(t, restarted.Reset_, "clearing older history restarts the view")
	require.Equal(t, keys[5:], restarted.EntryKeys)
	require.False(t, restarted.GetMore())

	// An unreadable creating entry fails the read instead of silently dropping the origin.
	_, err = sessions.DeleteSession(ctx, "chat")
	require.NoError(t, err)
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = db.ExecContext(ctx, `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('chat', '{"output_trace":1}', '')`)
	require.NoError(t, err)
	appendTurn("readable")

	_, err = server.history(ctx, &HistoryRequest{Id: "chat", Limit: 1})
	require.ErrorContains(t, err, "read origin entry")

	canceled, cancel := context.WithCancel(ctx)
	cancel()

	_, err = server.history(canceled, &HistoryRequest{Id: "cron:chat", Limit: 1})
	require.ErrorContains(t, err, "read web history page")
}

func TestHistoryPublicProgress(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}
	sessions, err := backend.NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })
	require.NoError(t, sessions.UpsertThread("public", backend.ThreadState{Agent: "main"}))
	server := &Server{sessions: sessions, cfg: cfg, usernames: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1"))
	checkpoint := &rocketcode.ActiveTurnCheckpoint{TurnID: "turn", ConversationKey: "public", Agent: "main", DisplayModel: "root/model", ReasoningEffort: new("high"), ReplayInput: []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","input_id":"input","content":"ask"}`),
		json.RawMessage(`{"type":"function_call","call_id":"A","name":"task","arguments":"{}"}`),
		json.RawMessage(`{"type":"function_call","call_id":"B","name":"execute","arguments":"{}"}`),
		json.RawMessage(`{"type":"function_call","call_id":"C","name":"task","arguments":"{}"}`),
	}, OutputTrace: []json.RawMessage{
		json.RawMessage(`{"type":"legacy","text":"PRIVATE CHILD SENTINEL"}`),
		json.RawMessage(`{"type":"rocketcode_public_progress","progress":{"id":"msg/0","parent_id":"turn/response","kind":"text","state":"working","text":"held partial suffix","agent":"main","model":"root/model"}}`),
		json.RawMessage(`{"type":"rocketcode_public_progress","progress":{"id":"A","parent_id":"turn/response","kind":"delegation","state":"working","agent":"canonical-child","model":"child/model"}}`),
		json.RawMessage(`{"type":"rocketcode_public_progress","progress":{"id":"B","parent_id":"turn/response","kind":"tool","state":"completed","text":"B result","agent":"main","model":"root/model"}}`),
		json.RawMessage(`{"type":"rocketcode_public_progress","progress":{"id":"C","parent_id":"turn/response","kind":"delegation","state":"blocked","agent":"canonical-child","model":"child/model"}}`),
		json.RawMessage(`{"type":"rocketcode_public_progress","progress":{"id":"unknown","kind":"private","state":"working","text":"PRIVATE CHILD SENTINEL"}}`),
	}}
	require.NoError(t, sessions.UpsertActiveTurn(ctx, checkpoint, nil))
	held, err := server.history(ctx, &HistoryRequest{Id: "public"})
	require.NoError(t, err)
	require.True(t, held.Running)

	texts := make([]string, len(held.Messages))
	for i, message := range held.Messages {
		texts[i] = message.Text
		require.Empty(t, message.MessageId)
	}

	require.Equal(t, []string{"ask", "held partial suffix", "task\n{}", "execute\n{}", "B result", "task\n{}", "blocked"}, texts)
	require.Equal(t, "turn:public:turn:turn/response/msg/0", held.Messages[1].ItemId)
	require.False(t, held.Messages[1].Complete)
	require.True(t, held.Messages[4].Complete, "B finishes independently while A and parent remain active")
	require.Equal(t, []string{"working", "completed", "blocked"}, []string{held.Messages[2].State, held.Messages[3].State, held.Messages[5].State})
	require.Equal(t, "canonical-child", held.Messages[2].Agent)
	require.Equal(t, "child/model", held.Messages[2].Model)
	require.Nil(t, held.Messages[2].ReasoningEffort)
	require.Equal(t, "main", held.Messages[1].Agent)
	require.Equal(t, "root/model", held.Messages[1].Model)
	require.Equal(t, new("high"), held.Messages[1].ReasoningEffort)
	require.Equal(t, "canonical", held.Messages[1].Origin)
	require.Empty(t, held.Messages[1].InputId)

	filtered, err := server.history(ctx, &HistoryRequest{Id: "public", SourceConversationId: "unrelated"})
	require.NoError(t, err)
	require.Empty(t, filtered.Messages)

	encoded, err := json.Marshal(held)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "PRIVATE CHILD SENTINEL")

	// Joining preserves the canonical rejected result, but it must never reach Web.
	checkpoint.ReplayInput = append(checkpoint.ReplayInput,
		json.RawMessage(`{"type":"function_call_output","call_id":"B","output":"B result"}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"C","output":"REVIEWER SENTINEL"}`))
	require.NoError(t, sessions.UpsertActiveTurn(ctx, checkpoint, nil))
	joined, err := server.history(ctx, &HistoryRequest{Id: "public", Revision: held.Revision})
	require.NoError(t, err)
	encoded, err = json.Marshal(joined)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "REVIEWER SENTINEL")
	require.Len(t, joined.Messages, 7)
	require.Equal(t, held.Messages[4].ItemId, joined.Messages[5].ItemId, "individual outcome keeps its identity after the canonical batch joins")

	for _, final := range []string{"short", "different", ""} {
		t.Run("final="+final, func(t *testing.T) {
			// Identity, not equal text, determines overlap. Distinct items retain equal text.
			checkpoint.ReplayInput = slices.Clone(checkpoint.ReplayInput[:6])
			raw, err := json.Marshal(struct {
				Type    string `json:"type"`
				ID      string `json:"id"`
				Role    string `json:"role"`
				Status  string `json:"status"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			}{Type: "message", ID: "msg", Role: "assistant", Status: "completed", Content: []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{{"output_text", final}}})
			require.NoError(t, err)

			checkpoint.ReplayInput = append(checkpoint.ReplayInput, raw)
			require.NoError(t, sessions.UpsertActiveTurn(ctx, checkpoint, nil))
			view, err := server.history(ctx, &HistoryRequest{Id: "public"})
			require.NoError(t, err)

			var replies []*TranscriptEvent

			for _, message := range view.Messages {
				if message.Role == "assistant" {
					replies = append(replies, message)
				}
			}

			if final == "" {
				require.Empty(t, replies)
			} else {
				require.Len(t, replies, 1)
				require.Equal(t, final, replies[0].Text)
				require.Equal(t, held.Messages[1].ItemId, replies[0].ItemId)
			}
		})
	}

	checkpoint.ReplayInput = append(checkpoint.ReplayInput,
		json.RawMessage(`{"type":"message","id":"opaque/segment","role":"assistant","status":"completed","content":[{"type":"refusal","refusal":"PRIVATE REFUSAL SENTINEL"},{"type":"output_text","text":" \n"},{"type":"output_text","text":"same"},{"type":"output_text","text":"same"}]}`),
		json.RawMessage(`{"type":"message","id":"another","role":"assistant","status":"completed","content":[{"type":"output_text","text":"same"}]}`))
	checkpoint.OutputTrace = append(checkpoint.OutputTrace, json.RawMessage(`{"type":"rocketcode_public_progress","progress":{"id":"opaque/segment/2","parent_id":"turn/response","kind":"text","state":"working","text":"same stale partial","agent":"main","model":"root/model"}}`))
	require.NoError(t, sessions.UpsertActiveTurn(ctx, checkpoint, nil))
	view, err := server.history(ctx, &HistoryRequest{Id: "public"})
	require.NoError(t, err)

	var replies []*TranscriptEvent

	for _, message := range view.Messages {
		if message.Role == "assistant" {
			replies = append(replies, message)
		}
	}

	require.Len(t, replies, 3, "equal text from distinct items/content indices is not deduplicated")
	require.Equal(t, []string{"same", "same", "same"}, []string{replies[0].Text, replies[1].Text, replies[2].Text})
	require.Equal(t, []string{"turn:public:turn:turn/response/opaque/segment/2", "turn:public:turn:opaque/segment/3", "turn:public:turn:another/0"}, []string{replies[0].ItemId, replies[1].ItemId, replies[2].ItemId})
	require.True(t, replies[0].Complete, "native completed text overrides the partial lifecycle")
	require.Equal(t, "completed", replies[0].State)
	require.Equal(t, "main", replies[0].Agent)
	require.Equal(t, "root/model", replies[0].Model)
	require.Equal(t, new("high"), replies[0].ReasoningEffort)

	entry := &rocketcode.SessionEntry{Type: "turn", TurnID: checkpoint.TurnID, Agent: checkpoint.Agent, Model: checkpoint.DisplayModel, ReasoningEffort: checkpoint.ReasoningEffort, ReplayInput: checkpoint.ReplayInput, OutputTrace: checkpoint.OutputTrace}
	_, err = sessions.AppendEntryID(ctx, "public", entry)
	require.NoError(t, err)

	for range 2 {
		view, err := server.history(ctx, &HistoryRequest{Id: "public"})
		require.NoError(t, err)
		require.False(t, view.Running)
		encoded, err := json.Marshal(view)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "REVIEWER SENTINEL")
		require.NotContains(t, string(encoded), "held partial suffix")
		require.NotContains(t, string(encoded), "PRIVATE REFUSAL SENTINEL")
		require.NotContains(t, string(encoded), "same stale partial")

		var savedReplies []*TranscriptEvent

		for _, message := range view.Messages {
			if message.Role == "assistant" {
				savedReplies = append(savedReplies, message)
			}
		}

		require.Len(t, savedReplies, len(replies))

		for i, message := range savedReplies {
			require.Equal(t, replies[i].ItemId, message.ItemId)
			require.Equal(t, replies[i].Text, message.Text)
			require.Equal(t, replies[i].Agent, message.Agent)
			require.Equal(t, replies[i].Model, message.Model)
			require.Equal(t, replies[i].ReasoningEffort, message.ReasoningEffort)
		}

		require.NoError(t, sessions.ClearActiveTurn(ctx, checkpoint.TurnID))
	}

	stored, err := sessions.ObserveEntries(ctx, "public")
	require.NoError(t, err)
	require.Equal(t, entry.ReplayInput, stored[0].Entry.ReplayInput, "projection never changes canonical reviewer-bearing replay")
	require.Equal(t, entry.OutputTrace, stored[0].Entry.OutputTrace, "unknown/private traces remain stored without being rendered")

	for _, outcome := range []rocketcode.PublicProgressState{rocketcode.PublicProgressBlocked, rocketcode.PublicProgressFailed, rocketcode.PublicProgressStopped, rocketcode.PublicProgressCompleted} {
		t.Run("delegation="+string(outcome), func(t *testing.T) {
			id := "delegation-" + string(outcome)
			require.NoError(t, sessions.UpsertThread(id, backend.ThreadState{Agent: "main"}))
			progress, err := json.Marshal(rocketcode.PublicProgress{ID: "child", ParentID: "turn/response", Kind: rocketcode.PublicProgressDelegation, State: outcome, Agent: "child", Model: "child/model"})
			require.NoError(t, err)

			result := "<task_result>delegation response blocked: inter-agent guardrail failed: REVIEWER_PRIVATE_SENTINEL CHILD_FAILURE_PRIVATE_SENTINEL</task_result>"
			if outcome == rocketcode.PublicProgressFailed {
				result = "<task_result>delegation failed: CHILD_FAILURE_PRIVATE_SENTINEL</task_result>"
			}

			if outcome == rocketcode.PublicProgressCompleted {
				result = "<task_result>approved child result</task_result>"
			}

			rawResult, err := json.Marshal(result)
			require.NoError(t, err)

			turn := &rocketcode.ActiveTurnCheckpoint{TurnID: id, ConversationKey: id, Agent: "main", ReplayInput: []json.RawMessage{
				json.RawMessage(`{"type":"function_call","call_id":"child","name":"task","arguments":"{}"}`),
			}, OutputTrace: []json.RawMessage{json.RawMessage(`{"type":"rocketcode_public_progress","progress":` + string(progress) + `}`)}}
			require.NoError(t, sessions.UpsertActiveTurn(ctx, turn, nil))
			turn.ReplayInput = append(turn.ReplayInput, json.RawMessage(`{"type":"function_call_output","call_id":"child","output":`+string(rawResult)+`}`))
			saved := &rocketcode.SessionEntry{Type: "turn", TurnID: id, Agent: "main", ReplayInput: turn.ReplayInput, OutputTrace: turn.OutputTrace}

			for _, stage := range []string{"joined", "saved", "reopened"} {
				switch stage {
				case "joined":
					require.NoError(t, sessions.UpsertActiveTurn(ctx, turn, nil))
				case "saved":
					_, err := sessions.AppendEntryID(ctx, id, saved)
					require.NoError(t, err)
				case "reopened":
					require.NoError(t, sessions.ClearActiveTurn(ctx, id))
				}

				view, err := server.history(ctx, &HistoryRequest{Id: id})
				require.NoError(t, err)
				encoded, err := json.Marshal(view)
				require.NoError(t, err)
				require.NotContains(t, string(encoded), "REVIEWER_PRIVATE_SENTINEL", stage)
				require.NotContains(t, string(encoded), "CHILD_FAILURE_PRIVATE_SENTINEL", stage)
				require.Len(t, view.Messages, 2, stage)
				require.Equal(t, string(outcome), view.Messages[1].State, stage)

				if outcome == rocketcode.PublicProgressCompleted {
					require.Contains(t, view.Messages[1].Text, "approved child result", stage)
				} else {
					require.Equal(t, string(outcome), view.Messages[1].Text, stage)
				}
			}

			stored, err := sessions.ObserveEntries(ctx, id)
			require.NoError(t, err)
			require.Equal(t, saved.ReplayInput, stored[0].Entry.ReplayInput)
		})
	}

	// Compaction can remove replay, but only typed public trace is a display fallback.
	checkpoint.TurnID = "compacted"
	checkpoint.ReplayInput = []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","input_id":"parked-steer","content":"steer at retained boundary"}`),
		json.RawMessage(`{"type":"message","id":"current","role":"assistant","status":"completed","content":[{"type":"output_text","text":"current canonical answer"}]}`),
		json.RawMessage(`{"type":"message","role":"user","input_id":"later-steer","content":"steer after current answer"}`),
	}
	checkpoint.OutputTrace = []json.RawMessage{
		json.RawMessage(`{"type":"rocketcode_public_progress","progress":{"id":"kept/0","parent_id":"compacted/response","kind":"text","state":"completed","text":"approved public fallback","agent":"main","model":"root/model"}}`),
		json.RawMessage(`{"type":"private_child","text":"PRIVATE CHILD SENTINEL"}`),
		json.RawMessage(`{"type":"rocketcode_public_progress","progress":{"id":"repeat","parent_id":"compacted/old","kind":"tool","state":"completed","text":"older tool outcome","agent":"main","model":"root/model"}}`),
		json.RawMessage(`{"type":"rocketcode_public_progress","progress":{"id":"repeat","parent_id":"compacted/new","kind":"delegation","state":"blocked","agent":"child","model":"child/model"}}`),
		json.RawMessage(`{"type":"rocketcode_public_progress","progress":{"id":"current/0","parent_id":"compacted/current","kind":"text","state":"completed","text":"current canonical answer with stale suffix","agent":"main","model":"root/model"}}`),
	}
	require.NoError(t, sessions.UpsertActiveTurn(ctx, checkpoint, nil))
	view, err = server.history(ctx, &HistoryRequest{Id: "public"})
	require.NoError(t, err)

	compacted := slices.DeleteFunc(slices.Clone(view.Messages), func(message *TranscriptEvent) bool { return message.TurnId != checkpoint.TurnID })

	texts = make([]string, 0, len(compacted))
	for _, message := range compacted {
		texts = append(texts, message.Text)
		require.Empty(t, message.MessageId)
	}

	require.Equal(t, []string{"steer at retained boundary", "approved public fallback", "older tool outcome", "blocked", "current canonical answer", "steer after current answer"}, texts)
	require.Equal(t, []string{"parked-steer", "later-steer"}, []string{compacted[0].InputId, compacted[5].InputId})
	require.Equal(t, []string{"turn:public:compacted:compacted/old/repeat:outcome", "turn:public:compacted:compacted/new/repeat:outcome"}, []string{compacted[2].ItemId, compacted[3].ItemId})
	require.True(t, compacted[1].Complete)

	entry = &rocketcode.SessionEntry{Type: "turn", TurnID: checkpoint.TurnID, Agent: checkpoint.Agent, Model: checkpoint.DisplayModel, ReplayInput: checkpoint.ReplayInput, OutputTrace: checkpoint.OutputTrace}
	entryID, err := sessions.AppendEntryID(ctx, "public", entry)
	require.NoError(t, err)

	for range 2 {
		view, err := server.history(ctx, &HistoryRequest{Id: "public"})
		require.NoError(t, err)

		saved := slices.DeleteFunc(slices.Clone(view.Messages), func(message *TranscriptEvent) bool { return message.TurnId != checkpoint.TurnID })
		require.Len(t, saved, len(compacted))

		for i, message := range saved {
			require.Equal(t, compacted[i].Text, message.Text)
			require.Equal(t, compacted[i].ItemId, message.ItemId)
		}

		require.Equal(t, fmt.Sprintf("%d:0", entryID), saved[0].MessageId)
		require.Equal(t, fmt.Sprintf("%d:1", entryID), saved[4].MessageId)
		require.Equal(t, fmt.Sprintf("%d:2", entryID), saved[5].MessageId)

		encoded, err := json.Marshal(view)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "PRIVATE CHILD SENTINEL")
		require.NotContains(t, string(encoded), "stale suffix")
		require.NoError(t, sessions.ClearActiveTurn(ctx, checkpoint.TurnID))
	}

	checkpoint.TurnID = "terminal-compacted"
	checkpoint.ReplayInput = nil
	checkpoint.OutputTrace = checkpoint.OutputTrace[:1]

	checkpoint.OutputTrace[0] = json.RawMessage(`{"type":"rocketcode_public_progress","progress":{"id":"kept/0","parent_id":"compacted/response","kind":"text","state":"working","text":"unfinished public fallback","agent":"main","model":"root/model"}}`)
	for _, terminal := range []protocol.Terminal{protocol.TerminalFailed, protocol.TerminalStopped} {
		require.NoError(t, sessions.UpsertActiveTurn(ctx, checkpoint, nil))
		require.NoError(t, sessions.SetActiveTurnTerminal(ctx, checkpoint.TurnID, terminal))
		view, err = server.history(ctx, &HistoryRequest{Id: "public"})
		require.NoError(t, err)

		last := view.Messages[len(view.Messages)-1]
		require.Equal(t, "unfinished public fallback", last.Text)
		require.Equal(t, string(terminal), last.State)
		require.False(t, last.Complete)
		require.False(t, view.Running)
	}
}
