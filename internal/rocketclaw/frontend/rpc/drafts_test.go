package rpc

import (
	"context"
	"database/sql"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
)

// People limited to spaces are refused by every agent editor method and by a draft's stream. Two editors
// joined on a draft's stream are woken by each other's changes and places, read them since their
// revision, and never lose either one's text.
func TestAgentDraftRPCs(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), Permissions: map[string][]string{"alice@example.com": {"Ops"}}}
	sessions, err := backend.NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })
	require.NoError(t, os.MkdirAll(filepath.Join(cfg.Workspace, cfg.RuntimeDirName(), "agents"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(cfg.Workspace, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Workspace, "agents", "support.md"), []byte("---\nmodel: gpt-5\n---\nHelp.\n"), 0o600))

	listener, err := net.Listen("unix", testSocketPath(t))
	require.NoError(t, err)

	transport := grpc.NewServer()
	core := &mockBackend{SandboxErrorFunc: noSandboxError, StashQueueItemFunc: func(context.Context, string, *protocol.ThreadQueueItem) error { return nil }}
	New(core, sessions, config.NewLockedConfig(cfg), &mockChannels{}, &mockCronJobs{}).Register(transport)

	var serving errgroup.Group
	serving.Go(func() error { return transport.Serve(listener) })
	t.Cleanup(func() { transport.Stop(); require.NoError(t, serving.Wait()) })

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	person := func(login string) context.Context {
		return metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-tailnet-login-bin", login, "rocketclaw-tailnet-name-bin", login))
	}
	bob, carol := person("bob@example.com"), person("carol@example.com")

	drafts := map[string]*AgentDraft{}
	for _, name := range []string{"helper", "support", "pushed", "saved"} {
		drafts[name], err = invoke[AgentDraft](bob, connection, "CreateAgentDraft", &CreateAgentDraftRequest{Name: name, Mode: "easy"})
		require.NoError(t, err)
	}

	for name, state := range map[string]string{"pushed": "waiting", "saved": "live"} {
		applied, err := invoke[ApplyAgentDraftOpResponse](bob, connection, "ApplyAgentDraftOp", &ApplyAgentDraftOpRequest{Id: drafts[name].Id, Op: &AgentDraftOp{BaseRevision: 1, OpId: "bob-" + name, Kind: "set_save_state", Value: state}})
		require.NoError(t, err)
		require.Equal(t, int64(2), applied.Revision)
	}

	listed, err := invoke[ListAgentDraftsResponse](bob, connection, "ListAgentDrafts", &ListAgentDraftsRequest{})
	require.NoError(t, err)
	require.Equal(t, backend.AgentDraftTemplates(), listed.Templates)

	type state struct {
		name, saveState string
		existing        bool
	}

	states := make([]state, 0, len(listed.Drafts))
	for _, draft := range listed.Drafts {
		states = append(states, state{draft.Name, draft.SaveState, draft.Existing})
	}

	require.Equal(t, []state{{"saved", "live", false}, {"pushed", "waiting", false}, {"support", "editing", true}, {"helper", "editing", false}}, states, "newest first")

	// A limited person gets the read-only Agents page only: the editor's methods and stream refuse them over HTTP.
	whois := &mockTailnetWhoIs{WhoIsFunc: func(context.Context, string) (*apitype.WhoIsResponse, error) {
		return &apitype.WhoIsResponse{Node: &tailcfg.Node{}, UserProfile: &tailcfg.UserProfile{LoginName: "alice@example.com"}}, nil
	}}
	handler := NewTailnetHandler(whois, nil, NewHTTPHandler(connection, config.SentryConfig{}, config.PostHogConfig{}, nil, "", nil))

	for _, method := range []string{"TestModel", "CreateAgentDraft", "ListAgentDrafts", "GetAgentDraft", "ApplyAgentDraftOp", "SetAgentDraftPresence", "AgentSettings", "DraftPrompt", "RestartDraftChat"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/"+method, strings.NewReader(`{}`)))
		require.Equal(t, http.StatusForbidden, response.Code, "%s: %s", method, response.Body)
		require.Contains(t, response.Body.String(), "people limited to spaces cannot use the agent editor", method)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/stream?id=draft:"+drafts["helper"].Id, http.NoBody))
	require.Equal(t, http.StatusForbidden, response.Code)
	require.Contains(t, response.Body.String(), "people limited to spaces cannot use the agent editor")

	id := drafts["helper"].Id
	join := func(ctx context.Context) grpc.ClientStream {
		stream, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/Join")
		require.NoError(t, err)
		require.NoError(t, stream.SendMsg(&JoinRequest{Id: "draft:" + id}))
		require.NoError(t, stream.CloseSend())
		require.NoError(t, stream.RecvMsg(&ConversationChange{})) // The opening wake-up, sent once the stream listens.

		return stream
	}
	streams := []grpc.ClientStream{join(bob), join(carol)}
	woken := func() {
		t.Helper()

		for _, stream := range streams {
			change := &ConversationChange{}
			require.NoError(t, stream.RecvMsg(change))
			require.Equal(t, "draft:"+id, change.ConversationId)
		}
	}

	apply := func(ctx context.Context, request *ApplyAgentDraftOpRequest, want *ApplyAgentDraftOpResponse) {
		t.Helper()

		request.Id = id
		applied, err := invoke[ApplyAgentDraftOpResponse](ctx, connection, "ApplyAgentDraftOp", request)
		require.NoError(t, err)
		require.True(t, proto.Equal(want, applied), "%v", applied)
	}
	get := func(ctx context.Context, since int64) *GetAgentDraftResponse {
		t.Helper()

		got, err := invoke[GetAgentDraftResponse](ctx, connection, "GetAgentDraft", &GetAgentDraftRequest{Id: id, Since: since})
		require.NoError(t, err)

		return got
	}
	ops := func(got *GetAgentDraftResponse) (out []*AgentDraftOp) {
		for _, op := range got.Ops {
			out = append(out, &AgentDraftOp{Revision: op.Revision, BaseRevision: op.BaseRevision, OpId: op.OpId, Author: op.Author, Kind: op.Kind, Variant: op.Variant, Splices: op.Splices})
		}

		return out
	}

	// The typist's cursor follows their own text.
	apply(bob, &ApplyAgentDraftOpRequest{Op: &AgentDraftOp{BaseRevision: 1, OpId: "bob-1", Kind: "edit_body", Variant: "a", Splices: []*AgentDraftSplice{{Insert: "world"}}}}, &ApplyAgentDraftOpResponse{Revision: 2, Cursor: 5})
	woken()

	caughtUp := get(carol, 1)
	require.Len(t, caughtUp.Ops, 1)
	require.True(t, proto.Equal(&AgentDraftOp{Revision: 2, BaseRevision: 1, OpId: "bob-1", Author: "bob@example.com", Kind: "edit_body", Variant: "a", Splices: []*AgentDraftSplice{{Insert: "world"}}}, ops(caughtUp)[0]), "%v", caughtUp.Ops[0])
	require.NotEmpty(t, caughtUp.Ops[0].At)
	require.Equal(t, "world", caughtUp.Draft.Variants[0].Body)

	// Both type on revision 2: carol's "!" after "world" lands after bob's "Hello " too, and so does her cursor.
	apply(bob, &ApplyAgentDraftOpRequest{Op: &AgentDraftOp{BaseRevision: 2, OpId: "bob-2", Kind: "edit_body", Variant: "a", Splices: []*AgentDraftSplice{{Insert: "Hello "}}}}, &ApplyAgentDraftOpResponse{Revision: 3, Cursor: 6})
	woken()
	apply(carol, &ApplyAgentDraftOpRequest{Cursor: 5, Op: &AgentDraftOp{BaseRevision: 2, OpId: "carol-1", Kind: "edit_body", Variant: "a", Splices: []*AgentDraftSplice{{At: 5, Insert: "!"}}}}, &ApplyAgentDraftOpResponse{Revision: 4, Cursor: 12})
	woken()

	since := get(bob, 2)
	require.Equal(t, "Hello world!", since.Draft.Variants[0].Body)
	require.Equal(t, int64(4), since.Draft.Revision)

	wantOps := []*AgentDraftOp{
		{Revision: 3, BaseRevision: 2, OpId: "bob-2", Author: "bob@example.com", Kind: "edit_body", Variant: "a", Splices: []*AgentDraftSplice{{Insert: "Hello "}}},
		{Revision: 4, BaseRevision: 2, OpId: "carol-1", Author: "carol@example.com", Kind: "edit_body", Variant: "a", Splices: []*AgentDraftSplice{{At: 11, Insert: "!"}}},
	}
	require.Len(t, since.Ops, len(wantOps))

	for i, op := range ops(since) {
		require.True(t, proto.Equal(wantOps[i], op), "%v", op)
	}

	// A file that does not parse yet stores nothing and tells only the typist its line.
	apply(carol, &ApplyAgentDraftOpRequest{Op: &AgentDraftOp{BaseRevision: 4, OpId: "carol-2", Kind: "edit_file", Variant: "a", Value: "---\nmodel: a\nmodel: b\n---\n"}}, &ApplyAgentDraftOpResponse{InvalidLine: 3})
	require.Empty(t, get(bob, 4).Ops)

	// A place wakes the teammates' streams; one not refreshed for 30 seconds is gone.
	_, err = invoke[SetAgentDraftPresenceResponse](carol, connection, "SetAgentDraftPresence", &SetAgentDraftPresenceRequest{Id: id, Presence: &AgentDraftPresence{Client: "carol-tab", Username: "mallory@example.com", Variant: "a", Place: "12"}})
	require.NoError(t, err)
	woken()

	present := get(bob, 4).Presence
	require.Len(t, present, 1)
	require.True(t, proto.Equal(&AgentDraftPresence{Client: "carol-tab", Username: "carol@example.com", Variant: "a", Place: "12"}, present[0]), "%v", present[0])

	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.ExecContext(t.Context(), `UPDATE agent_draft_presence SET seen_at = seen_at - interval '31 seconds'`)
	require.NoError(t, err)
	require.Empty(t, get(bob, 4).Presence)

	// The setting help is the loader's table for the asked mode, in its order.
	for _, mode := range []harness.AgentMode{harness.AgentModeEasy, harness.AgentModeAdvanced} {
		help, err := invoke[AgentSettingsResponse](bob, connection, "AgentSettings", &AgentSettingsRequest{Mode: string(mode)})
		require.NoError(t, err)

		var want, got []string

		for _, setting := range harness.AgentSettings() {
			if slices.Contains(setting.Modes, mode) {
				want = append(want, setting.Key)
			}
		}

		for _, setting := range help.Settings {
			got = append(got, setting.Key)
		}

		require.Equal(t, want, got)
		require.Equal(t, mode == harness.AgentModeAdvanced, slices.Contains(got, "description"))

		space := help.Settings[slices.Index(got, "space")]
		require.Equal(t, mode == harness.AgentModeEasy, space.Required)
		require.Equal(t, map[harness.AgentMode]string{harness.AgentModeEasy: "None: an easy agent must name a space.", harness.AgentModeAdvanced: "Chats start in whatever space they are opened from."}[mode], space.DefaultText)
		require.Equal(t, "A space name, as one string.", space.Allowed)
		require.Equal(t, "text", space.Control)
		require.Equal(t, "Basics", space.Group)
	}

	// A draft whose own settings do not load is refused with them, and nothing is sent.
	refused, err := invoke[DraftPromptResponse](bob, connection, "DraftPrompt", &DraftPromptRequest{Id: id, Text: "hello"})
	require.NoError(t, err)
	require.Equal(t, "Fix the draft's settings before testing it.", refused.Refused)
	require.NotEmpty(t, refused.Problems)
	require.Equal(t, "a", refused.Problems[0].Variant)
	require.Empty(t, core.StashQueueItemCalls())

	// The first message creates the test chat and goes to it as the caller's Web message; a restart gives the
	// draft a new chat, whose first message here is an External MCP call: its pairs only become variables, and
	// the caller is its only principal.
	probe, err := invoke[AgentDraft](bob, connection, "CreateAgentDraft", &CreateAgentDraftRequest{Name: "probe", Mode: "advanced"})
	require.NoError(t, err)

	applyProbe := func(op *AgentDraftOp) {
		t.Helper()

		_, err := invoke[ApplyAgentDraftOpResponse](bob, connection, "ApplyAgentDraftOp", &ApplyAgentDraftOpRequest{Id: probe.Id, Op: op})
		require.NoError(t, err)
	}
	applyProbe(&AgentDraftOp{BaseRevision: 1, OpId: "bob-probe-1", Kind: "edit_file", Variant: "a", Value: "---\nmodel: gpt-6.1-sol\n---\nHelp.\n"})

	sent, err := invoke[DraftPromptResponse](bob, connection, "DraftPrompt", &DraftPromptRequest{Id: probe.Id, Text: "hello"})
	require.NoError(t, err)
	require.True(t, proto.Equal(&DraftPromptResponse{TestChat: sent.TestChat}, sent), "%v", sent)
	require.NotEmpty(t, sent.TestChat)

	web := core.StashQueueItemCalls()[0]
	require.Equal(t, sent.TestChat, web.S)
	require.Equal(t, "hello", web.ThreadQueueItem.Inbound.Text)
	require.Equal(t, protocol.SourceWeb, web.ThreadQueueItem.Inbound.Source)
	require.Equal(t, protocol.InboundKindEnqueue, web.ThreadQueueItem.Inbound.Kind)
	require.Equal(t, "bob@example.com", web.ThreadQueueItem.Inbound.Metadata[protocol.InboundPrincipalEmailMetadataKey])

	restarted, err := invoke[DraftPromptResponse](bob, connection, "RestartDraftChat", &RestartDraftChatRequest{Id: probe.Id})
	require.NoError(t, err)
	require.NotEqual(t, sent.TestChat, restarted.TestChat)
	require.Len(t, core.StashQueueItemCalls(), 1)

	current, err := invoke[GetAgentDraftResponse](bob, connection, "GetAgentDraft", &GetAgentDraftRequest{Id: probe.Id})
	require.NoError(t, err)
	require.Equal(t, restarted.TestChat, current.Draft.TestChat)
	applyProbe(&AgentDraftOp{BaseRevision: current.Draft.Revision, OpId: "bob-probe-2", Kind: "set_external_mcp", ExternalMcp: &AgentDraftExternalMCP{On: true, Caller: "zendesk-bridge", Metadata: map[string]string{"customer_id": "acme-042"}}})

	_, err = invoke[DraftPromptResponse](bob, connection, "DraftPrompt", &DraftPromptRequest{Id: probe.Id, Text: "as a call"})
	require.NoError(t, err)

	call := core.StashQueueItemCalls()[1]
	require.Equal(t, restarted.TestChat, call.S)
	require.Equal(t, protocol.SourceExternalMCP, call.ThreadQueueItem.Inbound.Source)
	require.Equal(t, protocol.InboundKindPrompt, call.ThreadQueueItem.Inbound.Kind)
	require.Equal(t, map[string]string{"customer_id": "acme-042", "external_conversation_id": restarted.TestChat}, call.ThreadQueueItem.Inbound.ExternalMCPMetadata)

	shown := maps.Clone(call.ThreadQueueItem.Inbound.Metadata)
	maps.DeleteFunc(shown, func(key, _ string) bool { return strings.HasPrefix(key, "rocketclaw_agent") }) // The captured files.
	require.Equal(t, map[string]string{protocol.InboundPrincipalMetadataKey: "zendesk-bridge"}, shown)

	// A limited person cannot follow a draft's test chat.
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/stream?id="+restarted.TestChat, http.NoBody))
	require.Equal(t, http.StatusForbidden, response.Code)
	require.Contains(t, response.Body.String(), "people limited to spaces cannot use the agent editor")
}
