package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func receiveLiveSnapshot(t *testing.T, stream grpc.ClientStream, first ...*TranscriptEvent) *TranscriptEvent {
	t.Helper()

	var (
		assembled  strings.Builder
		snapshotID string
	)

	for index := uint32(0); ; index++ {
		event := new(TranscriptEvent)
		if index == 0 && len(first) > 0 {
			event = first[0]
		} else {
			require.NoError(t, stream.RecvMsg(event))
		}

		require.NotEmpty(t, event.Fragment, "expected a readable snapshot fragment, not compact content")
		require.LessOrEqual(t, proto.Size(event), 1<<20)

		if index == 0 {
			snapshotID = event.SnapshotId
		}

		require.Equal(t, snapshotID, event.SnapshotId)
		require.Equal(t, index, event.FragmentIndex)
		assembled.WriteString(event.Fragment)

		if event.SnapshotEnd {
			var snapshot TranscriptEvent
			require.NoError(t, protojson.Unmarshal([]byte(assembled.String()), &snapshot))

			return &snapshot
		}
	}
}

func liveTestConnection(t *testing.T, server *Server) *grpc.ClientConn {
	t.Helper()
	listener, err := Listen(testSocketPath(t))
	require.NoError(t, err)

	transport := grpc.NewServer()
	server.Register(transport)

	var serving errgroup.Group
	serving.Go(func() error { return transport.Serve(listener) })
	t.Cleanup(func() { transport.Stop(); require.NoError(t, serving.Wait()) })

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	return connection
}

func requireBrowserBoundary(t *testing.T, output *bufio.Scanner, want string) {
	t.Helper()

	for output.Scan() {
		if output.Text() == want {
			return
		}
	}

	require.NoError(t, output.Err())
	t.Fatalf("browser ended before %s", want)
}

func TestLiveReadableSnapshots(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	workspace := t.TempDir()
	sessions, err := backend.NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: workspace}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	const id = "slack-thread:C1:1.1"

	attachment := protocol.OutboundAttachment{ID: "file", Name: "image.png", MIMEType: "image/png", Data: []byte("private original bytes")}
	require.NoError(t, sessions.SaveAttachment(t.Context(), id, &attachment, false))

	entry := rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now(), ResponseID: "response", Agent: "planner", Model: "work/model", ReasoningEffort: new("high"), ReplayInputIDs: map[string]int{"web-one": 0, "web-two": 8}, ReplayInput: []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","prompt_header":"[Web principal=\"alice\"]","content":"[Web principal=\"alice\"]\n\nsame input"}`),
		json.RawMessage(`{"type":"reasoning","encrypted_content":"never expose","summary":[]}`),
		json.RawMessage(`{"type":"reasoning","summary":[{"type":"summary_text","text":"readable summary"}],"encrypted_content":"never expose"}`),
		json.RawMessage(`{"type":"function_call","call_id":"first","name":"execute","arguments":"{\"code\":\"first full script\"}"}`),
		json.RawMessage(`{"type":"function_call","call_id":"second","name":"execute","arguments":"{\"code\":\"second full script\"}"}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"first","output":"first complete output"}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"second","output":"second complete output"}`),
		json.RawMessage(`{"type":"message","role":"developer","content":"loaded skill instructions"}`),
		json.RawMessage(`{"type":"message","role":"user","prompt_header":"[Slack principal=\"alice\"]","content":"[Slack principal=\"alice\"]\n\nsame input"}`),
		json.RawMessage(`{"type":"function_call","call_id":"attach","name":"rocketclaw_attach_files_to_response","arguments":"{\"paths\":[\"secret/path\"]}"}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"attach","output":"queued attachments for final response: file"}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"attach","output":"queued attachments for final response: file"}`),
		json.RawMessage(`{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"checking files","annotations":[]}]}`),
		json.RawMessage(`{"type":"message","role":"assistant","phase":"final_answer","content":"recorded answer one"}`),
		json.RawMessage(`{"type":"message","role":"assistant","content":"recorded answer two"}`),
	}}
	entry.ReplayAttribution = []rocketcode.ReplayAttribution{{Start: 0, End: 7, Agent: "earlier", Model: "work/old", ReasoningEffort: new("low")}}
	checkpoint := rocketcode.ActiveTurnCheckpoint{TurnID: "storage-id", ConversationKey: id, ResponseID: entry.ResponseID, Agent: entry.Agent, DisplayModel: entry.Model, ReasoningEffort: entry.ReasoningEffort, ReplayInput: entry.ReplayInput, ReplayInputIDs: entry.ReplayInputIDs, ReplayAttribution: entry.ReplayAttribution}
	require.NoError(t, sessions.UpsertActiveTurn(t.Context(), &checkpoint, map[string]string{"execution_turn_id": "live-id"}))
	data, err := json.Marshal(checkpoint)
	require.NoError(t, err)

	stale := protocol.NewOutboundMessage(id, "lossy compact answer")
	stale.TurnID, stale.TranscriptCheckpoint, stale.ProgressText = "live-id", data, "lossy tool progress"
	// A publication can wait behind opening seed reads. Durable replay, not the
	// pending event's older bytes, is authoritative when that event is handled.
	older := checkpoint
	older.ReplayInput = checkpoint.ReplayInput[:5]
	stale.TranscriptCheckpoint, err = json.Marshal(older)
	require.NoError(t, err)

	consumed := protocol.NewOutboundMessage(id, "")
	consumed.ConsumedID, consumed.ConsumedText = "web-two", "same input"
	consumed.ConsumedHeader = `[Slack principal="alice"]`
	terminal := protocol.NewOutboundMessage(id, "")
	terminal.TurnID, terminal.TranscriptTerminal = "live-id", protocol.TerminalComplete
	terminal.Attachments = []protocol.OutboundAttachment{attachment}
	terminal.TranscriptEntry, err = json.Marshal(entry)
	require.NoError(t, err)

	compact := protocol.NewOutboundMessage(id, "must not duplicate recorded answers")
	compact.TurnID, compact.Complete = "live-id", true
	synced := protocol.NewOutboundMessage(id, "")
	synced.SourceConversationID = "cron:private"
	syncedEntry := entry
	syncedEntry.ReplayInput = slices.Clone(entry.ReplayInput)
	syncedEntry.ReplayInput[10] = json.RawMessage(`{"type":"function_call_output","call_id":"attach","output":"queued attachments for final response: synced-file"}`)
	syncedEntry.ReplayInput[11] = slices.Clone(syncedEntry.ReplayInput[10])
	synced.TranscriptEntry, err = json.Marshal(syncedEntry)
	require.NoError(t, err)

	attachment.ID = "synced-file"
	require.NoError(t, sessions.SaveAttachment(t.Context(), "cron:private", &attachment, false))

	synced.TranscriptEntryID = 99
	ignored := protocol.NewOutboundMessage("cron:private", "never expose private activity")

	acks := make([]chan error, 5)
	for i := range acks {
		acks[i] = make(chan error, 1)
	}

	core := &mockBackend{SubscribeFunc: func(context.Context) iter.Seq[protocol.Event] {
		return func(yield func(protocol.Event) bool) {
			for i, message := range []*protocol.OutboundMessage{consumed, stale, terminal, compact, synced, ignored} {
				ack := make(chan error, 1)
				if i < len(acks) {
					ack = acks[i]
				}

				if message == terminal {
					entryID, err := sessions.AppendEntryID(t.Context(), id, &entry)
					require.NoError(t, err)

					terminal.TranscriptEntryID = entryID

					require.NoError(t, sessions.ClearActiveTurn(t.Context(), checkpoint.TurnID))
				}

				if !yield(protocol.Event{Message: message, Acknowledgement: ack}) {
					return
				}
			}
		}
	}}
	server := New(core, sessions, &config.Config{Workspace: workspace, WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}, &mockChannels{}, &mockCronJobs{})
	connection := liveTestConnection(t, server)
	ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1"))
	stream, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/Join")
	require.NoError(t, err)
	require.NoError(t, stream.SendMsg(&JoinRequest{Id: id}))
	require.NoError(t, stream.CloseSend())
	seed := receiveLiveSnapshot(t, stream)
	require.True(t, seed.Seed)
	require.Empty(t, seed.Items)
	active := receiveLiveSnapshot(t, stream)
	require.Equal(t, "live-id", active.TurnId, "blocked-tool seed uses outbound identity, not checkpoint ID")
	require.Empty(t, active.Terminal)
	require.Equal(t, "web-one", active.Items[0].InputId)
	require.Equal(t, `[Web principal="alice"]`, active.Items[0].Header)
	require.Equal(t, "same input", active.Items[0].Text)
	require.Equal(t, "web-two", active.Items[7].InputId, "equal text does not merge distinct inputs")
	require.Equal(t, consumed.ConsumedHeader, active.Items[7].Header)
	require.Equal(t, "same input", active.Items[7].Text)
	require.Equal(t, "live-id:8", active.Items[7].MessageId)
	require.Equal(t, "earlier", active.Items[1].Agent)
	require.Equal(t, "work/old", active.Items[1].Model)
	require.Equal(t, new("low"), active.Items[1].ReasoningEffort)
	require.Equal(t, "planner", active.Items[7].Agent)
	require.Equal(t, "work/model", active.Items[7].Model)
	require.Equal(t, "commentary", active.Items[11].GetPhase())
	require.Equal(t, "checking files", active.Items[11].Text)
	require.Equal(t, "assistant", active.Items[11].Role)
	require.Equal(t, "final_answer", active.Items[12].GetPhase())
	require.Empty(t, active.Items[13].GetPhase(), "older assistant messages have no phase")
	require.Nil(t, active.Items[0].Phase, "user rows do not carry assistant phase")

	var consumption TranscriptEvent
	require.NoError(t, stream.RecvMsg(&consumption))
	require.Equal(t, "web-two", consumption.MessageId)
	require.Equal(t, "web-two", consumption.ConsumedId)
	require.Equal(t, consumed.ConsumedHeader, consumption.Header)
	require.Equal(t, "same input", consumption.Text)
	update := receiveLiveSnapshot(t, stream)
	require.Equal(t, active.Items, update.Items, "pending older publication cannot overwrite newer seed")
	finished := receiveLiveSnapshot(t, stream)
	require.Equal(t, "complete", finished.Terminal)
	require.Empty(t, finished.Text, "silent delivery does not create an answer bubble")
	history, err := server.history(metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1")), &HistoryRequest{Id: id})
	require.NoError(t, err)
	require.True(t, proto.Equal(history, &HistoryResponse{Messages: finished.Items}), "live items exactly equal History, including metadata and stored IDs")
	require.Len(t, finished.Items, 14)
	require.Equal(t, "commentary", finished.Items[11].GetPhase())
	require.Equal(t, "final_answer", finished.Items[12].GetPhase())
	require.Len(t, finished.Attachments, 1, "repeated attachment references produce one delivered file")
	require.Equal(t, "file", finished.Attachments[0].Id)
	require.Equal(t, id, finished.Attachments[0].ConversationId)
	require.Empty(t, finished.Attachments[0].Data, "terminal delivery exposes metadata, never raw bytes")
	require.Equal(t, "first", finished.Items[2].ToolCallId)
	require.Equal(t, "second", finished.Items[3].ToolCallId)
	require.Equal(t, "first complete output", finished.Items[4].Text)
	require.Equal(t, "second complete output", finished.Items[5].Text)

	for _, item := range finished.Items {
		require.True(t, item.Complete)
		require.Equal(t, "canonical", item.Origin)
		require.NotContains(t, item.Text, "never expose")
		require.NotContains(t, item.Text, "secret/path")

		for _, file := range item.Attachments {
			require.Empty(t, file.Data)
		}
	}

	copied := receiveLiveSnapshot(t, stream)
	require.Empty(t, copied.TurnId)
	require.Empty(t, copied.Terminal)
	require.Equal(t, int64(99), copied.EntryId)

	for _, item := range copied.Items {
		require.Equal(t, "sandboxed", item.Origin)
	}

	require.ErrorIs(t, stream.RecvMsg(&consumption), io.EOF)

	for _, ack := range acks {
		require.NoError(t, <-ack)
		require.Empty(t, ack, "each publication is acknowledged once")
	}
}

func TestSlackExecutionLiveReplay(t *testing.T) {
	// Exercise the real execution/checkpoint/bus/projection chain, not a manually
	// published transcript. The separate managed thread never reaches Slack's API.
	const id = "slack-thread:C-LIVE-TEST:1.1"

	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	started := make(chan string, 2)
	release := make(chan struct{})

	tools := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- r.URL.Path

		select {
		case <-release:
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprintf(w, "complete result %s", r.URL.Path)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(tools.Close)

	responses := make(chan string, 3)
	for _, output := range []string{
		fmt.Sprintf(`[{"type":"message","id":"comment","status":"completed","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Checking files","annotations":[]}]},{"type":"message","id":"before","status":"completed","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Before tools","annotations":[]}]},{"type":"reasoning","id":"reason","summary":[{"type":"summary_text","text":"Available thinking"}]},{"type":"function_call","id":"fc1","call_id":"first","name":"execute","arguments":%q},{"type":"function_call","id":"fc2","call_id":"second","name":"execute","arguments":%q},{"type":"function_call","id":"fc3","call_id":"skill","name":"skill","arguments":"{\"name\":\"live-test\"}"}]`, `{"code":"def main():\n    return webfetch(url=\"`+tools.URL+`/first\", format=\"text\")"}`, `{"code":"def main():\n    return webfetch(url=\"`+tools.URL+`/second\", format=\"text\")"}`),
		`[{"type":"message","id":"continued","status":"completed","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"After consumed Slack replies","annotations":[]}]}]`,
		`[]`,
	} {
		responses <- output
	}

	interruptReady := make(chan struct{})
	offlineRelease := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input []json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}

		if strings.Contains(string(request.Input[len(request.Input)-1]), "Interrupt Slack input") {
			close(interruptReady)
			<-r.Context().Done()

			return
		}

		if strings.Contains(string(request.Input[len(request.Input)-1]), "Failed Slack input") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"id":"failed-response","object":"response","status":"failed","model":"gpt-5.5","output":[],"error":{"code":"invalid_prompt","message":"failed request"}}`)

			return
		}

		if strings.Contains(string(request.Input[len(request.Input)-1]), "Offline Slack input") {
			select {
			case <-offlineRelease:
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"id":"offline-response","object":"response","status":"completed","model":"gpt-5.5","output":[{"type":"message","id":"offline-answer","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Completed while offline","annotations":[]}]}]}`)
			case <-r.Context().Done():
			}

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"resp-%d","object":"response","status":"completed","model":"gpt-5.5","output":%s}`, len(request.Input), <-responses)
	}))
	t.Cleanup(provider.Close)

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	cfg := &config.Config{DatabaseURL: dsn, Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: provider.URL}, WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}
	ready := make(chan *backend.Runtime, 1)
	runCtx, stopRuntime := context.WithCancel(t.Context())

	var running errgroup.Group
	running.Go(func() error {
		return backend.Run(runCtx, cfg, "", slog.New(slog.DiscardHandler), &mockFrontendAssembler{
			ValidateAssetsFunc: func(*config.Config, string, []string) error {
				// Runtime startup installs its skeleton. Restore this test's
				// local agent and skill through the same workspace root.
				if err := root.WriteFile(".rocketclaw/agents/main.md", []byte("---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\nreasoningEffort: high\npermission:\n  webfetch: allow\n  skill: allow\n---\nPrompt\n"), 0o644); err != nil {
					return fmt.Errorf("write live agent: %w", err)
				}

				if err := root.MkdirAll(".rocketclaw/skills/live-test", 0o755); err != nil {
					return fmt.Errorf("create live skill: %w", err)
				}

				return root.WriteFile(".rocketclaw/skills/live-test/SKILL.md", []byte("---\nname: live-test\ndescription: Integration skill\n---\nFull loaded skill instructions.\n"), 0o644)
			},
			AssembleFunc: func(runtime *backend.Runtime) (backend.SlackFrontend, <-chan struct{}, []func(context.Context) error, error) {
				ready <- runtime
				// Explicit inert frontend keeps this actual runtime offline.
				return &mockSlackFrontend{
					SetPendingSteersSinkFunc: func(protocol.PendingSteersSink) {},
					DrainSteersFunc:          func(context.Context, string) []string { return nil },
				}, runCtx.Done(), nil, nil
			},
		})
	})
	t.Cleanup(func() { stopRuntime(); require.NoError(t, running.Wait()) })

	runtime := <-ready
	sessions := runtime.Sessions
	require.NoError(t, runtime.CreateConversation(t.Context(), protocol.Conversation{ID: id, Agent: "main", CreatedBy: "alice"}))
	bridge := backend.NewConversation(cfg, runtime, &backend.Config{ConversationID: id, Agent: "main", SessionService: sessions}, slog.New(slog.DiscardHandler))
	require.NoError(t, bridge.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, bridge.Stop()) })
	// Sidebar and queue APIs are outside this execution test. Keep the stream
	// bound to the real bus while supplying their existing generated mocks.
	core := &mockBackend{
		SubscribeFunc:         runtime.Subscribe,
		ListConversationsFunc: runtime.ListConversations,
		QueueItemsFunc:        func(string) ([]protocol.ThreadQueueItem, error) { return nil, nil },
	}
	channels := &mockChannels{
		ChannelAgentChoicesFunc:        func(context.Context, string) ([]string, error) { return []string{"main"}, nil },
		SidebarChannelAgentChoicesFunc: func(context.Context, string) (string, []string, error) { return "Live test", []string{"main"}, nil },
	}
	jobs := &mockCronJobs{JobsFunc: func() ([]cronfrontend.Job, error) { return nil, nil }}
	connection := liveTestConnection(t, New(core, sessions, cfg, channels, jobs))

	ctx, cancel := context.WithCancel(metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1")))
	defer cancel()

	stream, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/Join")
	require.NoError(t, err)
	require.NoError(t, stream.SendMsg(&JoinRequest{Id: id}))
	require.NoError(t, stream.CloseSend())
	require.True(t, receiveLiveSnapshot(t, stream).Seed)

	var (
		browser       *exec.Cmd
		browserOutput *bufio.Scanner
		browserInput  io.WriteCloser
	)

	if os.Getenv("ROCKETCLAW_SLACK_BROWSER") == "1" {
		server := startHTTPTestServer(t, connection)
		browser = exec.CommandContext(t.Context(), "bun", "test", "src/slack-live.browser.test.ts")
		browser.Dir = "../../web"

		browser.Env = append(os.Environ(), "ROCKETCLAW_TEST_HTTP_URL="+server.URL, "ROCKETCLAW_LIVE_TEST_ID="+id)
		browser.Stderr = os.Stderr
		browserInput, err = browser.StdinPipe()
		require.NoError(t, err)
		stdout, err := browser.StdoutPipe()
		require.NoError(t, err)
		require.NoError(t, browser.Start())
		t.Cleanup(func() { _ = browser.Process.Kill() })

		browserOutput = bufio.NewScanner(stdout)
		requireBrowserBoundary(t, browserOutput, "browser-ready")
	}

	input := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "slack-root", "Slack idle input", true)
	input.ConversationID = id
	require.NoError(t, bridge.Submit(t.Context(), input))
	initial := receiveLiveSnapshot(t, stream)
	require.Len(t, initial.Items, 1, "initial checkpoint: %s", initial)
	require.Contains(t, initial.Items[0].Text, "Slack idle input")

	var calls *TranscriptEvent
	for {
		calls = receiveLiveSnapshot(t, stream)
		if slices.ContainsFunc(calls.Items, func(item *TranscriptEvent) bool { return item.ToolCallId == "second" }) {
			break
		}
	}

	require.Empty(t, calls.Terminal)

	for range 2 {
		<-started
	}

	// Reopen while both real tools are blocked, without waiting for another
	// provider response to restore the execution identity and readable calls.
	cancel()

	ctx, cancel = context.WithCancel(metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1")))
	defer cancel()

	stream, err = connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/Join")
	require.NoError(t, err)
	require.NoError(t, stream.SendMsg(&JoinRequest{Id: id}))
	require.NoError(t, stream.CloseSend())
	require.True(t, receiveLiveSnapshot(t, stream).Seed)
	restored := receiveLiveSnapshot(t, stream)
	require.Equal(t, calls.TurnId, restored.TurnId)
	require.True(t, proto.Equal(calls, restored), "blocked execution reopens with the same ordered active snapshot")

	if browser != nil {
		requireBrowserBoundary(t, browserOutput, "calls-restored")
	}

	for range 2 {
		steer := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindSteer, "slack-steer", "same Slack reply", true)
		steer.ConversationID = id
		require.NoError(t, bridge.Submit(t.Context(), steer))
	}

	close(release)

	var terminal *TranscriptEvent

	for {
		var frame TranscriptEvent
		require.NoError(t, stream.RecvMsg(&frame))

		if frame.SnapshotId == "" {
			continue
		} // Consumption announcements aren't replay insertion.

		snapshot := receiveLiveSnapshot(t, stream, &frame)
		if snapshot.Terminal != "" {
			terminal = snapshot
			break
		}
	}

	history, err := invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: id})
	require.NoError(t, err)
	require.True(t, proto.Equal(history, &HistoryResponse{Messages: terminal.Items}), "live final equals settled readable history")
	require.Equal(t, "complete", terminal.Terminal)

	var replies int

	texts := make([]string, 0, len(terminal.Items))

	for _, item := range terminal.Items {
		require.Equal(t, "canonical", item.Origin)
		require.Equal(t, "main", item.Agent)

		texts = append(texts, item.Text)
		if strings.Contains(item.Text, "same Slack reply") {
			replies++
		}
	}

	require.Equal(t, 2, replies, "identical Slack replies remain distinct")

	joined := strings.Join(texts, "\n")
	for _, text := range []string{"Before tools", "Available thinking", "complete result /first", "complete result /second", "Full loaded skill instructions.", "same Slack reply", "After consumed Slack replies"} {
		require.Contains(t, joined, text)
	}

	require.Less(t, strings.Index(joined, "complete result /second"), strings.Index(joined, "same Slack reply"))
	require.Less(t, strings.LastIndex(joined, "same Slack reply"), strings.Index(joined, "After consumed Slack replies"))

	if browser != nil {
		requireBrowserBoundary(t, browserOutput, "settled-equals-reload")
	}

	silent := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "silent-root", "Silent Slack input", true)
	silent.ConversationID = id
	require.NoError(t, bridge.Submit(t.Context(), silent))

	for {
		snapshot := receiveLiveSnapshot(t, stream)
		if snapshot.Terminal == "" {
			continue
		}

		require.Equal(t, "complete", snapshot.Terminal)
		require.Empty(t, snapshot.Text, "silent execution has no delivered answer")
		require.Len(t, snapshot.Items, 1, "silent execution retains just its input, not a blank answer")
		require.Contains(t, snapshot.Items[0].Text, "Silent Slack input")

		break
	}

	if browser != nil {
		requireBrowserBoundary(t, browserOutput, "silent-idle")
	}

	interrupt := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "interrupt-root", "Interrupt Slack input", true)
	interrupt.ConversationID = id
	require.NoError(t, bridge.Submit(t.Context(), interrupt))
	require.Contains(t, receiveLiveSnapshot(t, stream).Items[0].Text, "Interrupt Slack input")
	<-interruptReady
	require.NotNil(t, bridge.InterruptActiveTurn())

	for {
		snapshot := receiveLiveSnapshot(t, stream)
		if snapshot.Terminal == "" {
			continue
		}

		require.Equal(t, "stopped", snapshot.Terminal)
		require.Empty(t, snapshot.Text)
		require.Len(t, snapshot.Items, 2, "retain the runtime's recorded recovery notice")
		require.Equal(t, "user", snapshot.Items[0].Role)
		require.Equal(t, "developer", snapshot.Items[1].Role)
		require.Contains(t, snapshot.Items[1].Text, "previous runtime was interrupted")

		break
	}

	if browser != nil {
		requireBrowserBoundary(t, browserOutput, "interrupt-idle")
	}

	offline := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "offline-root", "Offline Slack input", true)
	offline.ConversationID = id
	require.NoError(t, bridge.Submit(t.Context(), offline))
	require.Contains(t, receiveLiveSnapshot(t, stream).Items[0].Text, "Offline Slack input")

	if browser != nil {
		requireBrowserBoundary(t, browserOutput, "disconnected")
	}

	close(offlineRelease)

	for {
		snapshot := receiveLiveSnapshot(t, stream)
		if snapshot.Terminal == "" {
			continue
		}

		require.Equal(t, "complete", snapshot.Terminal)
		require.Equal(t, "Completed while offline", snapshot.Items[len(snapshot.Items)-1].Text)

		break
	}

	if browser != nil {
		_, err := fmt.Fprintln(browserInput, "reconnect")
		require.NoError(t, err)
		requireBrowserBoundary(t, browserOutput, "reconnected")
	}

	const producerID = "private-live-producer"
	require.NoError(t, runtime.CreateConversation(t.Context(), protocol.Conversation{ID: producerID, Agent: "main"}))
	require.NoError(t, sessions.UpsertExternalMCPSession("private-live", &backend.ExternalMCPSessionState{PrivateConversationID: producerID, ManagedConversationID: id, Agent: "main"}))

	private := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "private-input", "Private producer input", false)
	private.ConversationID, private.SyncDestination = producerID, id

	responses <- `[ {"type":"reasoning","id":"private-thinking","summary":[{"type":"summary_text","text":"Private readable thinking"}]}, {"type":"function_call","id":"private-call","call_id":"private-tool","name":"skill","arguments":"{\"name\":\"live-test\"}"} ]`

	responses <- `[]`

	require.NoError(t, runtime.RunTurn(t.Context(), private))

	privateHistory, err := invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: producerID})
	require.Error(t, err, "private producer is not a Web history endpoint")
	require.Nil(t, privateHistory)

	beforeSync, err := invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: id})
	require.NoError(t, err)
	require.NotContains(t, beforeSync.String(), "Private readable thinking")

	if browser != nil {
		_, err := fmt.Fprintln(browserInput, "private-finished")
		require.NoError(t, err)
		requireBrowserBoundary(t, browserOutput, "private-hidden")
	}

	require.NoError(t, runtime.SyncConversation(t.Context(), producerID, id))
	synced := receiveLiveSnapshot(t, stream)
	require.Empty(t, synced.Terminal, "sync is not destination turn termination")

	for _, item := range synced.Items {
		require.Equal(t, "sandboxed", item.Origin)
		require.Equal(t, "main", item.Agent)
		require.Equal(t, "openai/gpt-5.5", item.Model)
		require.Equal(t, new("high"), item.ReasoningEffort)
	}

	afterSync, err := invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: id})
	require.NoError(t, err)
	require.Contains(t, afterSync.String(), "Private readable thinking")
	require.NoError(t, runtime.SyncConversation(t.Context(), producerID, id))
	repeatedSync, err := invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: id})
	require.NoError(t, err)
	require.True(t, proto.Equal(afterSync, repeatedSync), "repeat sync adds no duplicate entry")

	if browser != nil {
		require.NoError(t, browser.Wait())
	}

	cancel()
	// Reopen only after the real failure has finished and its live terminal is
	// gone. Retained recovery data must not resurrect an executing/busy turn.
	failed := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "", "Failed Slack input", true)
	failed.ConversationID = id
	require.ErrorContains(t, runtime.RunTurn(t.Context(), failed), "failed request")

	for range 2 {
		joined, leave := context.WithCancel(metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1")))
		reopened, err := connection.NewStream(joined, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/Join")
		require.NoError(t, err)
		require.NoError(t, reopened.SendMsg(&JoinRequest{Id: id}))
		require.NoError(t, reopened.CloseSend())
		require.True(t, receiveLiveSnapshot(t, reopened).Seed)
		retained := receiveLiveSnapshot(t, reopened)
		require.Equal(t, "failed", retained.Terminal)
		require.Contains(t, retained.Items[0].Text, "Failed Slack input")
		leave()

		responses <- `[]`

		later := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "", "Later Slack input", true)
		later.ConversationID = id
		require.NoError(t, runtime.RunTurn(t.Context(), later))
	}
}

func TestPromptAndLiveTransport(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	sessions, err := backend.NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	rt := &backend.Runtime{Sessions: sessions}
	entered := make(chan *protocol.InboundMessage)
	release := make(chan struct{})
	stashed := make(chan *protocol.ThreadQueueItem, 1)
	subscribed := make(chan struct{}, 1)
	unsubscribed := make(chan struct{}, 1)
	core := &mockBackend{
		StashQueueItemFunc: func(_ context.Context, conversationID string, item *protocol.ThreadQueueItem) error {
			item.ConversationID = conversationID
			stashed <- item

			return nil
		},
		RunTurnFunc: func(ctx context.Context, inbound *protocol.InboundMessage) error {
			select {
			case entered <- inbound:
			case <-ctx.Done():
				return ctx.Err()
			}

			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}

			if inbound.Text == "fail" {
				return errors.New("turn failed")
			}

			return nil
		},
		SubscribeFunc: func(ctx context.Context) iter.Seq[protocol.Event] {
			events := rt.Subscribe(ctx)

			subscribed <- struct{}{}

			return func(yield func(protocol.Event) bool) {
				events(yield)

				unsubscribed <- struct{}{}
			}
		},
	}
	listener, err := Listen(testSocketPath(t))
	require.NoError(t, err)

	transport := grpc.NewServer()
	New(core, rt.Sessions, &config.Config{Workspace: t.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}, &mockChannels{}, &mockCronJobs{}).Register(transport)

	var serving errgroup.Group
	serving.Go(func() error { return transport.Serve(listener) })
	t.Cleanup(func() {
		transport.Stop()
		require.NoError(t, serving.Wait())
	})

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1"))

	const id = "slack-thread:C1:1.1"

	var prompts errgroup.Group

	finished := make(chan struct{})

	prompts.Go(func() error {
		response, err := invoke[PromptResponse](ctx, connection, "Prompt", &PromptRequest{Id: id, Text: "exact input", Delivery: PromptDelivery_STEER})
		if err != nil {
			return err
		}

		assert.Empty(t, response.PrivateText)
		close(finished)

		return nil
	})

	inbound := <-entered
	require.Equal(t, id, inbound.ConversationID)
	require.Equal(t, protocol.SourceWeb, inbound.Source)
	require.Equal(t, "alice", inbound.Label)
	require.Equal(t, "alice", inbound.Metadata[protocol.InboundPrincipalMetadataKey])
	require.Equal(t, "exact input", inbound.Text)
	require.Equal(t, "exact input", inbound.Metadata[protocol.InboundRawTextMetadataKey])
	require.True(t, inbound.Human)
	require.Equal(t, protocol.InboundKindSteer, inbound.Kind)

	select {
	case <-finished:
		t.Fatal("Prompt returned before RunTurn completed")
	default:
	}

	release <- struct{}{}

	require.NoError(t, prompts.Wait())

	response, err := invoke[PromptResponse](ctx, connection, "Prompt", &PromptRequest{Id: id, Text: "queued input", Delivery: PromptDelivery_QUEUE})
	require.NoError(t, err)
	assert.Empty(t, response.PrivateText)

	item := <-stashed
	require.Equal(t, id, item.ConversationID)
	require.Equal(t, protocol.SourceWeb, item.Source)
	require.Equal(t, "alice", item.Principal)
	require.Equal(t, "queued input", item.Message)
	require.Equal(t, protocol.InboundKindEnqueue, item.Kind)

	for _, inner := range []string{"$review \"first area\"  second\tthird  ", "$skill stop inspect  the logs\nnext"} {
		_, err := invoke[PromptResponse](ctx, connection, "Prompt", &PromptRequest{Id: id, Text: "$enqueue \t" + inner, Delivery: PromptDelivery_STEER})
		require.NoError(t, err)

		item := <-stashed
		require.Equal(t, inner, item.Message)
		require.Equal(t, inner, item.Content.Text)
		require.Equal(t, "alice", item.Principal)
	}

	var failure errgroup.Group
	failure.Go(func() error {
		_, err := invoke[PromptResponse](ctx, connection, "Prompt", &PromptRequest{Id: id, Text: "fail"})
		return err
	})
	<-entered

	release <- struct{}{}

	require.ErrorContains(t, failure.Wait(), "turn failed")

	for _, request := range []*PromptRequest{{}, {Id: id, Delivery: PromptDelivery(99)}} {
		_, err := invoke[PromptResponse](ctx, connection, "Prompt", request)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	}

	_, err = invoke[PromptResponse](metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "alice")), connection, "Prompt", &PromptRequest{Id: id})
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	// Subscribe is the real runtime's live-only, acknowledged event stream.
	require.NoError(t, rt.PublishOutbound(ctx, protocol.NewOutboundMessage(id, "old history")))

	liveCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := connection.NewStream(liveCtx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/Join")
	require.NoError(t, err)
	require.NoError(t, stream.SendMsg(&JoinRequest{Id: id}))
	require.NoError(t, stream.CloseSend())
	<-subscribed

	seed := receiveLiveSnapshot(t, stream)
	require.True(t, seed.Seed)

	for _, other := range []string{"private-X", "web-only", "slack-thread:C2:2.2"} {
		require.NoError(t, rt.PublishOutbound(ctx, protocol.NewOutboundMessage(other, "not this conversation")))
	}

	message := protocol.NewOutboundMessage(id, "answer")
	message.Agent, message.Model, message.ReasoningEffort, message.SourceConversationID = "planner", "work/model-a", new(""), id
	attachment := protocol.OutboundAttachment{ID: "live-image", Name: "image.png", MIMEType: "image/png", Data: []byte("original")}
	require.NoError(t, sessions.SaveAttachment(ctx, id, &attachment, false))
	_, err = sessions.AppendEntryID(ctx, id, &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now(), ReplayInput: []json.RawMessage{
		json.RawMessage(`{"type":"function_call","call_id":"live","name":"rocketclaw_attach_files_to_response","arguments":"{}"}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"live","output":"queued attachments for final response: live-image"}`),
	}})
	require.NoError(t, err)

	message.Attachments = []protocol.OutboundAttachment{attachment, {ID: "not-referenced", Data: []byte("do not expose")}}
	message.TurnID = "turn-one"
	message.ProgressText = "thinking"
	message.Complete = true
	require.NoError(t, rt.PublishOutbound(ctx, message))

	for _, want := range []*TranscriptEvent{{Text: "thinking", Role: "thinking"}, {Text: "answer", Role: "assistant", Complete: true}} {
		var got TranscriptEvent
		require.NoError(t, stream.RecvMsg(&got))
		require.Equal(t, want.Text, got.Text)
		require.Equal(t, want.Role, got.Role)
		require.Equal(t, want.Complete, got.Complete)
		require.False(t, got.Snapshot)
		require.Equal(t, "turn-one", got.TurnId)
		require.Equal(t, "planner", got.Agent)
		require.Equal(t, "work/model-a", got.Model)
		require.Equal(t, new(""), got.ReasoningEffort)
		require.Equal(t, "canonical", got.Origin)

		if got.Role == "assistant" {
			require.Len(t, got.Attachments, 1)
			require.Equal(t, attachment.ID, got.Attachments[0].Id)
			require.Equal(t, int64(len(attachment.Data)), got.Attachments[0].Size)
			require.Empty(t, got.Attachments[0].Data)
		}
	}

	message.Text, message.ProgressText = "", ""
	require.NoError(t, rt.PublishOutbound(ctx, message))

	var terminal TranscriptEvent
	require.NoError(t, stream.RecvMsg(&terminal))
	require.True(t, terminal.Complete)
	require.Empty(t, terminal.Text)
	cancel()
	require.Equal(t, codes.Canceled, status.Code(stream.RecvMsg(&terminal)))
	// Client cancellation does not wait for the server's subscription cleanup.
	<-unsubscribed
	require.NoError(t, rt.PublishOutbound(ctx, message))

	denied, err := connection.NewStream(t.Context(), &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/Join")
	require.NoError(t, err)
	require.NoError(t, denied.SendMsg(&JoinRequest{Id: id}))
	require.NoError(t, denied.CloseSend())
	require.Equal(t, codes.Unauthenticated, status.Code(denied.RecvMsg(&terminal)))
	require.NoError(t, sessions.UpsertExternalMCPSession("live-external", &backend.ExternalMCPSessionState{PrivateConversationID: "private-X", ManagedConversationID: id, Agent: "main", SlackChannel: "#ops"}))

	for _, private := range []string{"cron:hidden", "one-off-cron:hidden", "private-X"} {
		denied, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/Join")
		require.NoError(t, err)
		require.NoError(t, denied.SendMsg(&JoinRequest{Id: private}))
		require.NoError(t, denied.CloseSend())
		require.Equal(t, codes.PermissionDenied, status.Code(denied.RecvMsg(&terminal)))
	}

	var browser errgroup.Group
	browser.Go(func() error {
		<-subscribed

		outbound := protocol.NewOutboundMessage(id, "live browser answer")

		outbound.Agent, outbound.Model, outbound.ReasoningEffort, outbound.SourceConversationID = "planner", "work/model-a", new(""), id
		if err := rt.PublishOutbound(ctx, outbound); err != nil {
			return fmt.Errorf("publish browser event: %w", err)
		}

		inbound := <-entered
		assert.Equal(t, id, inbound.ConversationID)
		assert.Equal(t, "alice", inbound.Metadata[protocol.InboundPrincipalMetadataKey])
		assert.Equal(t, protocol.InboundKindSteer, inbound.Kind)

		release <- struct{}{}

		return nil
	})

	httpServer := startHTTPTestServer(t, connection)

	proxy := exec.CommandContext(t.Context(), "bun", "test", "src/live-transport.test.ts")
	proxy.Dir = "../../web"

	proxy.Env = append(os.Environ(), "ROCKETCLAW_TEST_HTTP_URL="+httpServer.URL, "ROCKETCLAW_LIVE_TEST_ID="+id)
	output, err := proxy.CombinedOutput()
	require.NoError(t, err, "%s", output)
	t.Log(string(output))
	require.NoError(t, browser.Wait())
}

func TestLiveControlAndSeedBoundaries(t *testing.T) {
	for _, mode := range []string{"legacy", "committed", "terminal-only", "terminal-delivery", "failed", "late-compact"} {
		t.Run(mode, func(t *testing.T) {
			dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
			require.NoError(t, err)
			cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}
			sessions, err := backend.NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

			const id = "slack-thread:C1:1.1"

			checkpoint := &rocketcode.ActiveTurnCheckpoint{TurnID: "storage", ConversationKey: id, ResponseID: "response", ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","content":"recorded answer"}`)}}

			sourceMetadata := map[string]string{"execution_turn_id": "execution"}
			if mode == "legacy" {
				delete(sourceMetadata, "execution_turn_id")
			}

			if mode != "terminal-only" && mode != "late-compact" {
				require.NoError(t, sessions.UpsertActiveTurn(t.Context(), checkpoint, sourceMetadata))
			}

			var entryID int64
			if mode == "committed" || mode == "late-compact" {
				entryID, err = sessions.AppendEntryID(t.Context(), id, &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now(), ResponseID: "response", ReplayInput: checkpoint.ReplayInput})
				require.NoError(t, err)
			}

			ack := make(chan error, 1)
			unsubscribed := make(chan struct{})
			core := &mockBackend{SubscribeFunc: func(ctx context.Context) iter.Seq[protocol.Event] {
				return func(yield func(protocol.Event) bool) {
					defer close(unsubscribed)

					if ctx.Err() != nil {
						return
					}

					terminal := protocol.NewOutboundMessage(id, "")

					terminal.TurnID, terminal.TranscriptTerminal = "execution", protocol.TerminalStopped
					if mode == "failed" {
						terminal.TranscriptTerminal = protocol.TerminalFailed
					}

					if mode == "terminal-delivery" {
						terminal.Text = "delivered answer"
					}

					if mode == "late-compact" {
						terminal.Text, terminal.Complete, terminal.TranscriptTerminal = "recorded answer", true, ""
						terminal.TranscriptEntryID = entryID
					}

					yield(protocol.Event{Message: terminal, Acknowledgement: ack})
				}
			}}
			connection := liveTestConnection(t, New(core, sessions, cfg, &mockChannels{}, &mockCronJobs{}))
			ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1"))
			stream, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/Join")
			require.NoError(t, err)
			require.NoError(t, stream.SendMsg(&JoinRequest{Id: id}))
			require.NoError(t, stream.CloseSend())
			seed := receiveLiveSnapshot(t, stream)
			require.True(t, seed.Seed)

			if mode == "terminal-only" || mode == "late-compact" {
				require.Equal(t, "complete", seed.Terminal, "an idle opening seed clears a terminal missed while disconnected")
			} else {
				require.Empty(t, seed.Terminal, "an active opening seed is not terminal")
			}

			if mode == "legacy" {
				require.Equal(t, codes.FailedPrecondition, status.Code(stream.RecvMsg(new(TranscriptEvent))))
				<-unsubscribed

				_, found, err := sessions.ConversationActiveTurn(t.Context(), id)
				require.NoError(t, err)
				require.True(t, found, "legacy reads fail explicitly without mutating or inventing identity")

				return
			}

			if mode == "late-compact" {
				require.Len(t, seed.Items, 1)
				require.ErrorIs(t, stream.RecvMsg(new(TranscriptEvent)), io.EOF, "joining after rich terminal but before ordinary final must not duplicate the seeded answer")

				return
			}

			if mode != "terminal-only" {
				active := receiveLiveSnapshot(t, stream)
				require.Equal(t, "execution", active.TurnId)
				require.Empty(t, active.Terminal)
				require.Equal(t, entryID, active.EntryId)

				if mode == "committed" {
					require.True(t, proto.Equal(seed.Items[0], active.Items[0]), "append/clear overlap binds the existing stored region")
				}
			}

			terminal := receiveLiveSnapshot(t, stream)
			require.False(t, terminal.Snapshot, "terminal-only control preserves already rendered items")
			require.Empty(t, terminal.Items)
			require.Equal(t, "execution", terminal.TurnId)

			want := "stopped"
			if mode == "failed" {
				want = "failed"
			}

			require.Equal(t, want, terminal.Terminal)

			if mode == "terminal-delivery" {
				require.Equal(t, "delivered answer", terminal.Text)
			}

			require.ErrorIs(t, stream.RecvMsg(new(TranscriptEvent)), io.EOF)
			<-unsubscribed
			require.NoError(t, <-ack)
			require.Empty(t, ack)
		})
	}
}

func TestLiveUpdateDuringSeed(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}
	sessions, err := backend.NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	const id = "slack-thread:C1:1.1"
	// A multi-frame history seed creates real transport backpressure. The test
	// persists/publishes an update after the first frame, without timing sleeps.
	output := json.RawMessage(fmt.Sprintf(`{"type":"function_call_output","call_id":"old","output":%q}`, strings.Repeat("old saved output\n", 400000)))
	_, err = sessions.AppendEntryID(t.Context(), id, &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now(), ReplayInput: []json.RawMessage{output}})
	require.NoError(t, err)

	checkpoint := &rocketcode.ActiveTurnCheckpoint{TurnID: "storage", ConversationKey: id, ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"input"}`)}}
	require.NoError(t, sessions.UpsertActiveTurn(t.Context(), checkpoint, map[string]string{"execution_turn_id": "execution"}))
	older, err := json.Marshal(checkpoint)
	require.NoError(t, err)

	runtime := &backend.Runtime{Sessions: sessions}
	unsubscribed := make(chan struct{})
	core := &mockBackend{SubscribeFunc: func(ctx context.Context) iter.Seq[protocol.Event] {
		events := runtime.Subscribe(ctx)

		return func(yield func(protocol.Event) bool) {
			defer close(unsubscribed)

			events(yield)
		}
	}}
	connection := liveTestConnection(t, New(core, sessions, cfg, &mockChannels{}, &mockCronJobs{}))

	ctx, cancel := context.WithCancel(metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1")))
	defer cancel()

	stream, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/Join")
	require.NoError(t, err)
	require.NoError(t, stream.SendMsg(&JoinRequest{Id: id}))
	require.NoError(t, stream.CloseSend())

	first := new(TranscriptEvent)
	require.NoError(t, stream.RecvMsg(first))
	require.False(t, first.SnapshotEnd)

	checkpoint.ReplayInput = append(checkpoint.ReplayInput, json.RawMessage(`{"type":"function_call","call_id":"blocked","name":"execute","arguments":"{\"code\":\"blocked full script\"}"}`))
	require.NoError(t, sessions.UpsertActiveTurn(t.Context(), checkpoint, map[string]string{"execution_turn_id": "execution"}))

	message := protocol.NewOutboundMessage(id, "")
	message.TurnID, message.TranscriptCheckpoint = "execution", older

	var publishing errgroup.Group
	publishing.Go(func() error { return runtime.PublishOutbound(t.Context(), message) })

	seed := receiveLiveSnapshot(t, stream, first)
	require.True(t, seed.Seed)
	require.Len(t, seed.Items, 1)
	active := receiveLiveSnapshot(t, stream)
	require.Equal(t, "execution", active.TurnId)
	require.Len(t, active.Items, 2)
	require.Equal(t, "blocked", active.Items[1].ToolCallId)
	update := receiveLiveSnapshot(t, stream)
	require.True(t, proto.Equal(active, update), "a publication waiting during seed cannot overwrite newer durable state")
	require.NoError(t, publishing.Wait())
	cancel()
	<-unsubscribed
	require.NoError(t, runtime.PublishOutbound(t.Context(), message))
}

func TestLiveSendErrorAcknowledgesAndUnsubscribes(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}
	sessions, err := backend.NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	const id = "slack-thread:C1:1.1"

	message := protocol.NewOutboundMessage(id, "")
	message.TurnID, message.TranscriptEntryID = "execution", 1
	message.TranscriptEntry, err = json.Marshal(&rocketcode.SessionEntry{ReplayInput: []json.RawMessage{json.RawMessage(fmt.Sprintf(`{"type":"function_call_output","call_id":"large","output":%q}`, strings.Repeat("full unread output\n", 500000)))}})
	require.NoError(t, err)

	ack := make(chan error, 2)
	unsubscribed := make(chan bool, 1)
	core := &mockBackend{SubscribeFunc: func(context.Context) iter.Seq[protocol.Event] {
		return func(yield func(protocol.Event) bool) {
			unsubscribed <- yield(protocol.Event{Message: message, Acknowledgement: ack})
		}
	}}
	connection := liveTestConnection(t, New(core, sessions, cfg, &mockChannels{}, &mockCronJobs{}))

	ctx, cancel := context.WithCancel(metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1")))
	defer cancel()

	stream, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/Join")
	require.NoError(t, err)
	require.NoError(t, stream.SendMsg(&JoinRequest{Id: id}))
	require.NoError(t, stream.CloseSend())
	require.True(t, receiveLiveSnapshot(t, stream).Seed)

	var first TranscriptEvent
	require.NoError(t, stream.RecvMsg(&first))
	require.False(t, first.SnapshotEnd)
	// Stop reading mid-snapshot. SendMsg observes cancellation while the next
	// fragments exceed the stream window; no artificial deadline is needed.
	cancel()
	require.False(t, <-unsubscribed, "a failed SendMsg stops the subscriber iterator")
	require.Error(t, <-ack)
	require.Empty(t, ack, "send failure is acknowledged exactly once")
}

func TestLiveProjectionErrorsAcknowledgeAndUnsubscribe(t *testing.T) {
	for _, mode := range []string{"entry", "checkpoint", "replay", "history-item", "replay-attachment", "history-input", "workspace", "encoding", "consumed", "progress", "response", "input-attachment", "output-attachment", "checkpoint-store"} {
		t.Run(mode, func(t *testing.T) {
			dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
			require.NoError(t, err)
			cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}
			sessions, err := backend.NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

			const id = "slack-thread:C1:1.1"

			message := protocol.NewOutboundMessage(id, "")
			message.TurnID = "execution"
			want := ""

			switch mode {
			case "entry":
				message.TranscriptEntry, want = []byte("{"), "decode live entry"
			case "checkpoint":
				message.TranscriptCheckpoint, want = []byte("{"), "decode live checkpoint"
				message.TranscriptTerminal = protocol.TerminalStopped
			case "replay":
				message.TranscriptEntry, want = []byte(`{"replay_input":[42]}`), "decode web history"
			case "history-item":
				message.TranscriptEntry, want = []byte(`{"replay_input":[{"type":"function_call_output","call_id":"tool","output":[{"type":"input_text","text":"readable output"}]}]}`), "project history attachments"
			case "replay-attachment":
				message.TranscriptEntry, want = []byte(`{"replay_input":[{"type":"function_call","call_id":"file","name":"rocketclaw_attach_files_to_response","arguments":"{}"},{"type":"function_call_output","call_id":"file","output":"queued attachments for final response: missing"}]}`), "project history attachments"
			case "history-input":
				message.TranscriptEntry, want = []byte(`{"replay_input":[{"type":"message","role":"user","content":"attachment:missing"}]}`), "load input attachment metadata"
			case "workspace":
				message.TranscriptEntry, want = []byte(`{}`), "open attachment workspace"
			case "encoding":
				message.TranscriptTerminal, message.Text, want = protocol.TerminalComplete, string([]byte{0xff}), "encode readable snapshot"
				message.TranscriptEntry = []byte(`{}`)
			case "consumed":
				message.ConsumedID, message.ConsumedText, want = "input", string([]byte{0xff}), "send consumed input"
			case "progress":
				message.ProgressText, want = string([]byte{0xff}), "send compact progress"
			case "response":
				message.Text, want = string([]byte{0xff}), "send compact response"
			case "input-attachment":
				message.ConsumedID, message.ConsumedText, want = "input", "attachment:missing", "load input attachment metadata"
			case "output-attachment":
				message.Text, message.Attachments, want = "answer", []protocol.OutboundAttachment{{ID: "file"}}, "database is closed"
			case "checkpoint-store":
				message.TranscriptCheckpoint, want = []byte(`{}`), "read live checkpoint"
			}

			ready := make(chan struct{})
			ack := make(chan error, 2)
			unsubscribed := make(chan bool, 1)
			core := &mockBackend{SubscribeFunc: func(context.Context) iter.Seq[protocol.Event] {
				return func(yield func(protocol.Event) bool) {
					<-ready

					unsubscribed <- yield(protocol.Event{Message: message, Acknowledgement: ack})
				}
			}}
			connection := liveTestConnection(t, New(core, sessions, cfg, &mockChannels{}, &mockCronJobs{}))
			ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1"))
			stream, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/Join")
			require.NoError(t, err)
			require.NoError(t, stream.SendMsg(&JoinRequest{Id: id}))
			require.NoError(t, stream.CloseSend())
			require.True(t, receiveLiveSnapshot(t, stream).Seed)

			if mode == "workspace" {
				require.NoError(t, os.Remove(cfg.Workspace))
			}

			if mode == "input-attachment" || mode == "output-attachment" || mode == "checkpoint-store" || mode == "history-input" {
				require.NoError(t, sessions.Stop())
			}

			close(ready)
			require.Error(t, stream.RecvMsg(new(TranscriptEvent)))
			require.False(t, <-unsubscribed)
			require.ErrorContains(t, <-ack, want)
			require.Empty(t, ack, "projection failure is acknowledged exactly once")
		})
	}
}

func TestLiveOpeningSeedFailureUnsubscribes(t *testing.T) {
	for _, mode := range []string{"store", "workspace"} {
		t.Run(mode, func(t *testing.T) {
			dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
			require.NoError(t, err)
			cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}
			sessions, err := backend.NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

			const id = "slack-thread:C1:1.1"

			want := ""

			switch mode {
			case "store":
				want = "database is closed"
			case "workspace":
				require.NoError(t, os.Remove(cfg.Workspace))

				want = "open attachment workspace"
			}

			ack := make(chan error, 2)
			unsubscribed := make(chan bool, 1)
			core := &mockBackend{SubscribeFunc: func(ctx context.Context) iter.Seq[protocol.Event] {
				if mode == "store" {
					// Visibility is checked before subscription; exercise a store
					// failure after that check, while opening the authorized seed.
					assert.NoError(t, sessions.Stop())
				}

				return func(yield func(protocol.Event) bool) {
					<-ctx.Done()

					unsubscribed <- yield(protocol.Event{Message: protocol.NewOutboundMessage(id, "unobserved"), Acknowledgement: ack})
				}
			}}
			connection := liveTestConnection(t, New(core, sessions, cfg, &mockChannels{}, &mockCronJobs{}))
			ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1"))
			stream, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/Join")
			require.NoError(t, err)
			require.NoError(t, stream.SendMsg(&JoinRequest{Id: id}))
			require.NoError(t, stream.CloseSend())
			require.ErrorContains(t, stream.RecvMsg(new(TranscriptEvent)), want)
			require.False(t, <-unsubscribed, "opening failure must still release the subscribed iterator")
			require.ErrorContains(t, <-ack, want)
			require.Empty(t, ack)
		})
	}
}
