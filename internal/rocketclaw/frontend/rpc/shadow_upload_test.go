package rpc

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestShadowUploadCardsAndDownloads(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}
	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll(filepath.Join(cfg.RuntimeDirName(), "agents", "main"), 0o700))

	for _, name := range []string{"main", "main/a"} {
		require.NoError(t, root.WriteFile(filepath.Join(cfg.RuntimeDirName(), "agents", name+".md"), []byte("---\nmodel: gpt-6.1-sol\n---\nReply."), 0o600))
	}

	sessions, err := backend.NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	runtime := &backend.Runtime{Sessions: sessions}
	require.NoError(t, runtime.CreateConversation(t.Context(), protocol.Conversation{ID: "parent", Agent: "main", CreatedBy: "alice"}))
	seeded := openTestTurns(t, dsn)

	var child string
	require.NoError(t, seeded.db.QueryRow(`SELECT child_conversation_id FROM shadow_bindings`).Scan(&child))

	server := New(withoutBackgroundJobs(), sessions, config.NewLockedConfig(cfg), &mockChannels{}, &mockCronJobs{})
	listener, err := net.Listen("unix", testSocketPath(t))
	require.NoError(t, err)

	transport := grpc.NewServer()
	server.Register(transport)

	var serving errgroup.Group
	serving.Go(func() error { return transport.Serve(listener) })
	t.Cleanup(func() { transport.Stop(); require.NoError(t, serving.Wait()) })

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	httpServer := httptest.NewServer(NewHTTPHandler(connection, cfg.Web.Sentry))
	t.Cleanup(httpServer.Close)
	ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1"))
	image, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aQ1sAAAAASUVORK5CYII=")
	require.NoError(t, err)

	var (
		files  []*Attachment
		replay []json.RawMessage
	)

	for i, data := range [][]byte{image, []byte("steer file"), []byte("unconsumed file")} {
		name := []string{"image.png", "steer.txt", "unconsumed.txt"}[i]
		response, err := http.Post(httpServer.URL+"/api/UploadAttachment?conversationId=parent&name="+name, "application/octet-stream", bytes.NewReader(data))
		require.NoError(t, err)
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Equal(t, http.StatusOK, response.StatusCode, string(body))

		file := new(Attachment)
		require.NoError(t, protojson.Unmarshal(body, file))
		files = append(files, file)

		if i == 2 {
			continue
		}

		content, err := server.uploadContent(metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1")), "parent", []string{"  initial image\n", "uploaded steer"}[i], []string{file.Id})
		require.NoError(t, err)

		text := content.Text + "\n\n" + content.TextAttachments[0]
		if i == 0 {
			require.Equal(t, []protocol.InboundAttachment{{Name: name, MIMEType: "image/png", Data: data}}, content.Attachments)
			replay = append(replay, json.RawMessage(fmt.Sprintf(`{"type":"message","role":"user","content":[{"type":"input_text","text":%q},{"type":"input_image","image_url":%q}]}`, text, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(data))))
		} else {
			replay = append(replay, json.RawMessage(fmt.Sprintf(`{"type":"message","role":"user","content":%q}`, text)))
		}
	}
	// Bridge-internal consumption writers persist these same provider inputs and turn correlation.
	_, err = sessions.AppendEntryID(t.Context(), "parent", &rocketcode.SessionEntry{Version: 1, Type: "turn", TurnID: "parent-turn", ReplayInput: replay})
	require.NoError(t, err)
	require.NoError(t, seeded.UpsertActiveTurn(t.Context(), &testCheckpoint{TurnID: "child-turn", ConversationKey: child, ReplayInput: replay}))
	_, err = seeded.db.Exec(`INSERT INTO shadow_turns (child_conversation_id, parent_source_conversation_id, parent_turn_id, child_turn_id, inbound_json) VALUES ($1, 'parent', 'parent-turn', 'child-turn', '{}')`, child)
	require.NoError(t, err)

	for _, completed := range []bool{false, true} {
		if completed {
			_, err = sessions.AppendEntryID(t.Context(), child, &rocketcode.SessionEntry{Version: 1, Type: "turn", TurnID: "child-turn", ReplayInput: replay})
			require.NoError(t, err)
			_, err = seeded.db.Exec(`DELETE FROM active_turns WHERE id = 'child-turn'`)
			require.NoError(t, err)
			_, err = seeded.db.Exec(`UPDATE shadow_turns SET input_closed = TRUE, terminal = 'complete', inputs = '[]', context_entries = '[]', environment = '{}', inbound_json = '{}'`)
			require.NoError(t, err)
		}

		view, err := invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: child})
		require.NoError(t, err)
		require.Len(t, view.Messages, 2)

		for i, message := range view.Messages {
			require.Equal(t, []string{"  initial image\n", "uploaded steer"}[i], message.Text)
			require.Len(t, message.Attachments, 1, "initial and steer cards, completed=%t", completed)
			require.Equal(t, files[i].Id, message.Attachments[0].Id)
			require.Equal(t, []string{"image/png", "text/plain"}[i], message.Attachments[0].MimeType)
			require.Equal(t, "parent", message.Attachments[0].ConversationId, "metadata records the actual owner")

			response, err := http.Get(httpServer.URL + "/api/DownloadAttachment?conversationId=" + child + "&id=" + files[i].Id)
			require.NoError(t, err)
			data, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, http.StatusOK, response.StatusCode, string(data))
			require.Equal(t, [][]byte{image, []byte("steer file")}[i], data)
		}
	}

	download, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/rpc.Web/DownloadAttachment")
	require.NoError(t, err)
	require.NoError(t, download.SendMsg(&Attachment{ConversationId: child, Id: files[0].Id}))
	require.NoError(t, download.CloseSend())

	frame := new(Attachment)
	require.NoError(t, download.RecvMsg(frame))
	require.Equal(t, child, frame.ConversationId, "download envelope retains its authorized child-history context")
	require.Equal(t, image, frame.Data)
	require.ErrorIs(t, download.RecvMsg(new(Attachment)), io.EOF)

	parentView, err := invoke[HistoryResponse](ctx, connection, "History", &HistoryRequest{Id: "parent"})
	require.NoError(t, err)
	require.Len(t, parentView.Messages, 2)
	entries, err := sessions.ObserveEntries(t.Context(), "parent")
	require.NoError(t, err)
	require.Equal(t, replay, entries[0].Entry.ReplayInput, "model input text and attachment references are untouched")
	require.NoError(t, sessions.UpsertExternalMCPSession("external", &backend.ExternalMCPSessionState{Agent: "main", PrivateConversationID: "parent", ManagedConversationID: "visible"}))
	require.NoError(t, runtime.CreateConversation(t.Context(), protocol.Conversation{ID: "visible", Agent: "main"}))

	for _, projection := range []struct {
		destination          string
		consumed, unconsumed int
	}{{"visible", http.StatusOK, http.StatusNotFound}, {"", http.StatusForbidden, http.StatusForbidden}} {
		_, err = seeded.db.Exec(`UPDATE managed_conversations SET producer_inbound_json = json_build_object('SyncDestination', $1::text) WHERE conversation_id = 'parent'`, projection.destination)
		require.NoError(t, err)

		for _, request := range []struct {
			id, file string
			status   int
		}{
			{child, files[0].Id, projection.consumed},
			{child, files[2].Id, projection.unconsumed},
			{"parent", files[0].Id, http.StatusForbidden},
		} {
			response, err := http.Get(httpServer.URL + "/api/DownloadAttachment?conversationId=" + request.id + "&id=" + request.file)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, request.status, response.StatusCode, "destination %q, history %q", projection.destination, request.id)
		}
	}

	response, err := http.Post(httpServer.URL+"/api/UploadAttachment?conversationId="+child+"&name=denied.txt", "text/plain", bytes.NewBufferString("must not upload"))
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusForbidden, response.StatusCode)
}
