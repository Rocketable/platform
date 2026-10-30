package rpc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestOpenCodeSessionsThroughTheWebRPC(t *testing.T) {
	var boundary string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()

		switch {
		case !ok || user != "opencode" || password != "pw":
			w.WriteHeader(http.StatusUnauthorized)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/session/ses_new":
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/api/session":
			_, _ = w.Write([]byte(`{"data":{"id":"ses_new","title":"","time":{"updated":5000},"location":{"directory":"/srv/app"}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/session/ses_new/fork":
			var body struct {
				Before string `json:"before"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			boundary = body.Before
			_, _ = w.Write([]byte(`{"data":{"id":"ses_fork","title":"Forked work","time":{"updated":6000},"location":{"directory":"/srv/app"}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)

	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()

	servers := map[string]config.OpenCodeConfig{
		"box":   {URL: upstream.URL, Password: "pw"},
		"down":  {URL: down.URL, Password: "pw"},
		"creds": {URL: "https://user:secret@box.example:4096/x?auth_token=t", Password: "pw"},
	}
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	sessions, err := backend.NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), OpenCode: servers}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	cfg := &config.Config{
		Workspace: t.TempDir(),
		WebUsers:  map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"},
		OpenCode:  servers,
	}
	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll(filepath.Join(cfg.RuntimeDirName(), "agents"), 0o700))
	require.NoError(t, root.WriteFile(filepath.Join(cfg.RuntimeDirName(), "agents", "main.md"), []byte("---\nmodel: gpt-6.1-sol\npermission:\n  rocketclaw:\n    rocketclaw_set_tag: [[customer, internal]]\n---\nHelp."), 0o600))

	server := New(&mockBackend{JustBashInstallErrorFunc: noJustBashError}, sessions, config.NewLockedConfig(cfg), &mockChannels{}, &mockCronJobs{})
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1"))

	spaces, err := sessions.Spaces(t.Context())
	require.NoError(t, err)

	created, err := server.createSession(ctx, &CreateSessionRequest{OpencodeServer: "box", Directory: "/srv/app", SpaceId: spaces[0].ID})
	require.NoError(t, err)
	assert.Equal(t, "opencode:box:ses_new", created.Id)

	_, err = server.createSession(ctx, &CreateSessionRequest{OpencodeServer: "nope", Directory: "/srv/app"})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = server.createSession(ctx, &CreateSessionRequest{OpencodeServer: "down", Directory: "/srv/app"})
	assert.Equal(t, codes.Unavailable, status.Code(err), "a server that does not answer")

	_, err = server.updateSession(ctx, &UpdateSessionRequest{Id: created.Id, Tag: new(" billing ")})
	require.NoError(t, err, "remote OpenCode sessions take any tag")

	_, err = server.updateSession(ctx, &UpdateSessionRequest{Id: created.Id, Tag: new(" ")})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "a blank tag")

	_, err = server.updateSession(ctx, &UpdateSessionRequest{Id: created.Id, Name: new(" Renamed ")})
	require.NoError(t, err, "a remote session is renamed on its server")

	require.NoError(t, sessions.UpsertThread("web", backend.ThreadState{Agent: "main"}))

	_, err = server.updateSession(ctx, &UpdateSessionRequest{Id: "web", Tag: new("customer")})
	require.NoError(t, err, "a tag the agent allows")

	_, err = server.updateSession(ctx, &UpdateSessionRequest{Id: "web", Tag: new("billing")})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "RocketClaw sessions take only tags their agent allows")

	_, err = server.prompt(ctx, &PromptRequest{Id: created.Id, Text: "hello"})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "a remote session is recorded from its creation, before any sync")

	_, err = openTestTurns(t, dsn).db.ExecContext(t.Context(), `INSERT INTO web_tabs (username, path, conversation_id, unread_at) VALUES ('alice', '/oc/a', $1, '2026-10-22T10:00:00Z')`, created.Id)
	require.NoError(t, err)

	var (
		rows []*Session
		done *ListSessionsResponse
	)

	require.NoError(t, server.listSessions(&ListSessionsRequest{}, &mockServerStream{ContextFunc: func() context.Context { return ctx }, SendMsgFunc: func(message any) error {
		response := message.(*ListSessionsResponse)
		rows = append(rows, response.Sessions...)

		if response.UpstreamSuccess {
			done = response
		}

		return nil
	}}))

	require.Len(t, rows, 1)
	assert.Equal(t, "box", rows[0].GetServer())
	assert.Equal(t, "/srv/app", rows[0].GetDirectory())
	assert.Equal(t, spaces[0].ID, rows[0].GetSpaceId(), "a remote session is born in the space the person was in")
	assert.Equal(t, []string{"billing"}, rows[0].GetTags())
	assert.Equal(t, "Renamed", rows[0].GetPreview(), "a remote row carries its title as its preview")
	assert.Equal(t, "Renamed", rows[0].GetName(), "the name is the server title")
	assert.False(t, rows[0].GetOffline())
	assert.Equal(t, "2026-10-22T10:00:00Z", rows[0].GetUnreadAt(), "a remote session carries the caller's mark")
	assert.True(t, proto.Equal(&ListSessionsResponse{
		Owner: "alice", UpstreamSuccess: true, Version: done.GetVersion(), Order: []string{created.Id},
		OpencodeServers: map[string]string{"box": upstream.URL, "down": down.URL, "creds": "https://box.example:4096"},
		AgentTags:       map[string]*TagChoices{"main": {Tags: []string{"customer", "internal"}}},
	}, done), "%v", done)

	// OpenCode v2 packages/app/src/session/commands/fork-dialog.tsx forks before a user message.
	// The new conversation must exist before the tab saves, not only after the next remote poll.
	forked, err := server.forkSession(ctx, &ForkSessionRequest{Id: created.Id, Before: "msg_boundary"})
	require.NoError(t, err)
	assert.Equal(t, "msg_boundary", boundary)
	assert.Equal(t, "opencode:box:ses_fork", forked.Id)

	var forkSpace string
	require.NoError(t, openTestTurns(t, dsn).db.QueryRowContext(t.Context(), `SELECT space_id FROM conversations WHERE conversation_id = $1`, forked.Id).Scan(&forkSpace))
	assert.Equal(t, spaces[0].ID, forkSpace, "a fork is born in its source's space")

	forkedFrom := map[string]string{}

	require.NoError(t, server.listSessions(&ListSessionsRequest{}, &mockServerStream{ContextFunc: func() context.Context { return ctx }, SendMsgFunc: func(message any) error {
		for _, row := range message.(*ListSessionsResponse).Sessions {
			forkedFrom[row.GetId()] = row.GetForkedFrom()
		}

		return nil
	}}))
	assert.Equal(t, map[string]string{created.Id: "", forked.Id: created.Id}, forkedFrom, "the Web lists a fork with its source")

	path := "/oc/" + base64.RawURLEncoding.EncodeToString([]byte(forked.Id))
	saved, err := sessions.SaveTabState(t.Context(), "alice", &backend.TabState{Space: spaces[0].ID, Tabs: []backend.Tab{{Path: path, Space: spaces[0].ID}}}, 0)
	require.NoError(t, err)
	require.True(t, saved)
	tabs, _, err := sessions.TabState(t.Context(), "alice")
	require.NoError(t, err)
	require.Equal(t, []backend.Tab{{Path: path, Space: spaces[0].ID}}, tabs.Tabs)

	for _, test := range []struct {
		id   string
		code codes.Code
	}{
		{"opencode:nope:ses_a", codes.InvalidArgument},
		{"opencode:box:", codes.InvalidArgument},
		{"opencode:down:ses_a", codes.Unavailable},
		{"opencode:box:missing", codes.Unavailable},
	} {
		_, err := server.forkSession(ctx, &ForkSessionRequest{Id: test.id})
		assert.Equal(t, test.code, status.Code(err), test.id)
	}

	_, err = server.forkSession(t.Context(), &ForkSessionRequest{Id: created.Id})
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}
