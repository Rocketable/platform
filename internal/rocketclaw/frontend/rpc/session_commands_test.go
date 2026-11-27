package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func branchSummaryRequest(body []byte) bool {
	return bytes.Contains(body, []byte("Summarize the abandoned conversation branch"))
}

func TestSummarizeBranch(t *testing.T) {
	t.Parallel()

	replies, requests := make(chan func(http.ResponseWriter), 1), make(chan string, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !assert.True(t, branchSummaryRequest(body), "only branch summaries reach this provider") {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}

		requests <- string(body)

		(<-replies)(w)
	}))
	t.Cleanup(provider.Close)

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	seeded := openTestTurns(t, dsn)
	cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), OpenAI: config.OpenAIConfig{APIKey: "test", APIBaseURL: provider.URL}}
	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll(filepath.Join(cfg.RuntimeDirName(), "agents"), 0o700))
	require.NoError(t, root.WriteFile(filepath.Join(cfg.RuntimeDirName(), "agents", "primary.md"), []byte("---\nmodel: gpt-6.1-sol\n---\nHelp."), 0o600))

	sessions, err := backend.NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })
	createWebChat(t, sessions, protocol.Conversation{ID: "chat", Agent: "primary"})

	server := &Server{sessions: sessions, cfg: config.NewLockedConfig(cfg), backend: withoutBackgroundJobs()}
	ctx := metadata.NewIncomingContext(t.Context(), tailnetUser("alice"))
	move := func(marker string) {
		_, err := seeded.db.ExecContext(ctx, `UPDATE conversations SET revert_message_id = $1 WHERE conversation_id = 'chat'`, marker)
		require.NoError(t, err)
	}
	appendEntry := func(user, assistant string) int64 {
		id, err := sessions.AppendEntryID(ctx, "chat", &harness.SessionEntry{Type: "turn", ReplayInput: []json.RawMessage{
			json.RawMessage(fmt.Sprintf(`{"type":"message","role":"user","content":%q}`, user)),
			json.RawMessage(fmt.Sprintf(`{"type":"message","role":"assistant","content":%q}`, assistant)),
		}})
		require.NoError(t, err)

		return id
	}
	answer := func(text string) func(http.ResponseWriter) {
		return func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"id":"summary","status":"completed","output":[{"type":"message","id":"msg","role":"assistant","status":"completed","content":[{"type":"output_text","text":%q,"annotations":[]}]}]}`, text)
		}
	}
	shown := func() []string {
		history, err := server.history(ctx, &HistoryRequest{Id: "chat"})
		require.NoError(t, err)

		texts := make([]string, 0, len(history.Messages))
		for _, message := range history.Messages {
			texts = append(texts, message.Header+message.Text)
		}

		return texts
	}

	appendEntry("A text", "a reply")
	c := appendEntry("C text", "c reply")
	move(fmt.Sprintf("%d:0", c))
	appendEntry("C prime", "c prime reply")

	branch := fmt.Sprintf("%d:0", c)

	replies <- answer("C went nowhere")

	summarized, err := server.summarizeBranch(ctx, &SummarizeBranchRequest{Id: "chat", BranchId: branch, Instructions: "Keep the numbers"})
	require.NoError(t, err)
	require.True(t, summarized.Appended)

	request := <-requests
	require.Contains(t, request, "Keep the numbers")
	require.Contains(t, request, "## user\\nC text")
	require.Contains(t, request, "## assistant\\nc reply")
	require.NotContains(t, request, "A text", "the branch starts at its split point")
	require.NotContains(t, request, "C prime")
	require.Equal(t, []string{"A text", "a reply", "C prime", "c prime reply", "[System]Summary of another branch of this chat:\n\nC went nowhere"}, shown())

	replies <- func(w http.ResponseWriter) { http.Error(w, "summary unavailable", http.StatusBadRequest) }

	_, err = server.summarizeBranch(ctx, &SummarizeBranchRequest{Id: "chat", BranchId: branch})
	require.ErrorContains(t, err, "generate web branch summary")
	require.NotContains(t, <-requests, "The person asking for the summary adds", "no instructions, no added line")
	require.Len(t, shown(), 5, "a failed summary appends nothing")

	replies <- func(w http.ResponseWriter) {
		move(fmt.Sprintf("%d:2", c))
		answer("too late")(w)
	}

	summarized, err = server.summarizeBranch(ctx, &SummarizeBranchRequest{Id: "chat", BranchId: branch})
	require.NoError(t, err)
	<-requests
	require.False(t, summarized.Appended, "the current path holds the branch by the time the summary finishes")
	require.Equal(t, []string{"A text", "a reply", "C text", "c reply"}, shown())

	_, err = server.summarizeBranch(ctx, &SummarizeBranchRequest{Id: "chat", BranchId: "9:0"})
	require.Equal(t, codes.NotFound, status.Code(err))

	move(fmt.Sprintf("%d:0", c))

	path := shown()
	running := func(started bool) {
		query := `DELETE FROM turns WHERE id = 'running'`
		if started {
			query = `INSERT INTO turns (id, conversation_id, inbound_json, output_trace_json, created_at_unix_ns, updated_at_unix_ns) VALUES ('running', 'chat', '{}', '[]', 0, 0)`
		}

		_, err := seeded.db.ExecContext(ctx, query)
		require.NoError(t, err)
	}

	running(true)

	_, err = server.summarizeBranch(ctx, &SummarizeBranchRequest{Id: "chat", BranchId: branch})
	require.ErrorContains(t, err, "conversation history is busy")
	require.Empty(t, requests, "no summary is generated while a turn runs")
	running(false)

	replies <- func(w http.ResponseWriter) {
		running(true)
		answer("while a turn ran")(w)
	}

	_, err = server.summarizeBranch(ctx, &SummarizeBranchRequest{Id: "chat", BranchId: branch})
	require.ErrorContains(t, err, "conversation history is busy")
	<-requests
	running(false)
	require.Equal(t, path, shown(), "a summary that finishes while a turn runs is not added")
}
