package rpc

import (
	"context"
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
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, password, ok := r.BasicAuth(); !ok || user != "opencode" || password != "pw" || r.URL.Path != "/api/session" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		_, _ = w.Write([]byte(`{"data":{"id":"ses_new","title":"","time":{"updated":5000},"location":{"directory":"/srv/app"}}}`))
	}))
	t.Cleanup(upstream.Close)

	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()

	servers := map[string]config.OpenCodeConfig{"box": {URL: upstream.URL, Password: "pw", Port: 4100}, "down": {URL: down.URL, Password: "pw", Port: 4101}}
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

	server := New(&mockBackend{}, sessions, config.NewLockedConfig(cfg), &mockChannels{}, &mockCronJobs{})
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1"))

	created, err := server.createSession(ctx, &CreateSessionRequest{OpencodeServer: "box", Directory: "/srv/app"})
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

	require.NoError(t, sessions.UpsertThread("web", backend.ThreadState{Agent: "main"}))

	_, err = server.updateSession(ctx, &UpdateSessionRequest{Id: "web", Tag: new("customer")})
	require.NoError(t, err, "a tag the agent allows")

	_, err = server.updateSession(ctx, &UpdateSessionRequest{Id: "web", Tag: new("billing")})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "RocketClaw sessions take only tags their agent allows")

	_, err = server.prompt(ctx, &PromptRequest{Id: created.Id, Text: "hello"})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "a remote session is recorded from its creation, before any sync")

	var (
		rows []*Session
		done *ListSessionsResponse
	)

	require.NoError(t, server.listSessions(&mockServerStream{ContextFunc: func() context.Context { return ctx }, SendMsgFunc: func(message any) error {
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
	assert.Equal(t, []string{"billing"}, rows[0].GetTags())
	assert.False(t, rows[0].GetOffline())
	assert.True(t, proto.Equal(&ListSessionsResponse{
		Owner: "alice", UpstreamSuccess: true,
		OpencodeServers: map[string]int32{"box": 4100, "down": 4101},
		AgentTags:       map[string]*TagChoices{"main": {Tags: []string{"customer", "internal"}}},
	}, done), "%v", done)
}
