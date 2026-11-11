package rpc

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const searchTab = "/search/page"

// blockedScan is a session search waiting in its room lookup, after its reads, until the test closes finish.
type blockedScan struct {
	ctx    context.Context
	finish chan struct{}
}

// searchRunTest is a server whose session searches each wait in their room lookup until the test finishes them, and
// Alice, who has the Search tab searchTab.
type searchRunTest struct {
	server   *Server
	sessions *backend.SessionService
	scans    chan blockedScan
	// alice is Alice's request context, and gone hers already cancelled: a request that starts or joins a run and
	// leaves at once.
	alice, gone context.Context
}

func newSearchRunTest(t *testing.T) *searchRunTest {
	t.Helper()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	sessions, err := backend.NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	const thread = "slack-thread:C1:1.1"
	require.NoError(t, sessions.UpsertThread(thread, backend.ThreadState{Agent: "main"}))
	_, err = sessions.AppendEntryID(t.Context(), thread, &harness.SessionEntry{})
	require.NoError(t, err)
	_, err = openTestTurns(t, dsn).db.ExecContext(t.Context(), `UPDATE conversations SET slack_channel = 'C1' WHERE conversation_id = $1`, thread)
	require.NoError(t, err)

	alice := metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-tailnet-login-bin", "alice@example.com", "rocketclaw-tailnet-name-bin", "Alice"))
	spaces, err := sessions.Spaces(alice)
	require.NoError(t, err)
	saved, err := sessions.SaveTabState(alice, "alice@example.com", &backend.TabState{Tabs: []backend.Tab{{Path: searchTab, Space: spaces[0].ID}}}, 0)
	require.NoError(t, err)
	require.True(t, saved)

	scans := make(chan blockedScan, searchRunsKept+1)
	server := &Server{sessions: sessions, runs: map[string][]*searchRun{}, channels: &mockChannels{SidebarChannelAgentChoicesFunc: func(ctx context.Context, _ string) (string, []string, error) {
		finish := make(chan struct{})
		scans <- blockedScan{ctx, finish}

		<-finish

		return "support", nil, nil
	}}}

	t.Cleanup(func() { require.NoError(t, server.StopSearches(), "every mark was written") })

	gone, cancel := context.WithCancel(alice)
	cancel()

	return &searchRunTest{server: server, sessions: sessions, scans: scans, alice: alice, gone: gone}
}

// searchRunRequest asks for the search id of searchTab with query, which the person entered or not.
func searchRunRequest(id, query string, entered bool) *SearchSessionsRequest {
	return &SearchSessionsRequest{Query: query, Tab: searchTab, SearchId: id, Entered: entered}
}

// scan is the next search to start, which fails the test when none starts.
func (rt *searchRunTest) scan(t *testing.T) blockedScan {
	t.Helper()

	select {
	case scan := <-rt.scans:
		return scan
	case <-time.After(10 * time.Second):
		t.Fatal("no search started")

		return blockedScan{}
	}
}

// leave sends request from a request that leaves at once.
func (rt *searchRunTest) leave(t *testing.T, request *SearchSessionsRequest) {
	t.Helper()

	_, err := rt.server.searchSessions(rt.gone, request)
	require.ErrorIs(t, err, context.Canceled)
}

// answer sends request in the background and returns its answer once it comes.
func (rt *searchRunTest) answer(request *SearchSessionsRequest) chan error {
	done := make(chan error, 1)

	go func() {
		response, err := rt.server.searchSessions(rt.alice, request)
		if err == nil && response.Needle != request.Query {
			err = fmt.Errorf("answer for %q, want %q", response.Needle, request.Query)
		}

		done <- err
	}()

	return done
}

// marks lists Alice's marks on tabs that are not sessions, whether each is quiet, and its note, then reads them.
func (rt *searchRunTest) marks(t *testing.T) []string {
	t.Helper()

	attention, err := rt.sessions.TabAttention(rt.alice, "alice@example.com")
	require.NoError(t, err)

	var marks []string

	for _, mark := range attention {
		if !mark.UnreadAt.IsZero() {
			marks = append(marks, fmt.Sprintf("%s quiet=%t: %s", mark.Path, mark.Quiet, mark.Note))
			require.NoError(t, rt.sessions.ChangeAttention(rt.alice, "alice@example.com", []string{mark.Path}, backend.AttentionRead, nil))
		}
	}

	return marks
}

// A search entered and left runs on, marks its tab when it finishes, and its answer is handed to the next request
// without searching again, once.
func TestSearchRunOutlivesItsRequest(t *testing.T) {
	rt := newSearchRunTest(t)

	rt.leave(t, searchRunRequest("s", "a b", true))
	scan := rt.scan(t)
	require.NoError(t, scan.ctx.Err(), "the run outlives its request")
	close(scan.finish)

	select {
	case err := <-rt.answer(searchRunRequest("s", "a b", false)):
		require.NoError(t, err)
	case <-rt.scans:
		t.Fatal("a kept answer is not searched again")
	}

	assert.Equal(t, []string{searchTab + " quiet=false: Search finished"}, rt.marks(t))

	again := rt.answer(searchRunRequest("s", "a b", false))

	select {
	case scan = <-rt.scans:
	case err := <-again:
		t.Fatalf("an answer is handed out once, got %v", err)
	}

	close(scan.finish)
	require.NoError(t, <-again)
	assert.Empty(t, rt.marks(t), "a search the person did not enter marks nothing")

	// The page opening runs its query; entering that query twice while it runs joins the run, which marks once.
	// Entering it after its answer was handed out runs it again, and marks again.
	rt.leave(t, searchRunRequest("s", "a b", false))
	scan = rt.scan(t)
	rt.leave(t, searchRunRequest("s", "a b", true))
	rt.leave(t, searchRunRequest("s", "a b", true))
	close(scan.finish)

	select {
	case err := <-rt.answer(searchRunRequest("s", "a b", false)):
		require.NoError(t, err)
	case <-rt.scans:
		t.Fatal("entering the running query joins its run")
	}

	assert.Equal(t, []string{searchTab + " quiet=false: Search finished"}, rt.marks(t))

	entered := rt.answer(searchRunRequest("s", "a b", true))
	scan = rt.scan(t)
	close(scan.finish)
	require.NoError(t, <-entered)
	assert.Equal(t, []string{searchTab + " quiet=false: Search finished"}, rt.marks(t))

	// Alice views the tab as the search finishes.
	rt.leave(t, searchRunRequest("s", "a b", true))
	scan = rt.scan(t)
	_, err := rt.sessions.PollAttention(rt.alice, "alice@example.com", searchTab, false)
	require.NoError(t, err)
	close(scan.finish)
	require.NoError(t, <-rt.answer(searchRunRequest("s", "a b", false)))
	assert.Empty(t, rt.marks(t), "the person viewing the tab is not marked")

	// Alice closes the tab before the search finishes.
	_, err = rt.sessions.PollAttention(rt.alice, "alice@example.com", "/cron", false)
	require.NoError(t, err)
	rt.leave(t, searchRunRequest("s", "a b", true))
	scan = rt.scan(t)
	saved, err := rt.sessions.SaveTabState(rt.alice, "alice@example.com", &backend.TabState{}, 1)
	require.NoError(t, err)
	require.True(t, saved)
	close(scan.finish)
	require.NoError(t, <-rt.answer(searchRunRequest("s", "a b", false)))
	assert.Empty(t, rt.marks(t), "a closed tab is not marked")
}

// A run replaced by another query or evicted by a ninth search is cancelled, and marks nothing even when its scan
// completes.
func TestSearchRunReplacedOrEvictedMarksNothing(t *testing.T) {
	rt := newSearchRunTest(t)

	replaced := rt.answer(searchRunRequest("s", "a b", true))
	first := rt.scan(t)
	rt.leave(t, searchRunRequest("s", "c d", true))
	assert.Equal(t, searchEndError("replaced"), context.Cause(first.ctx))

	second := rt.scan(t)

	close(first.finish)
	require.NoError(t, <-replaced, "the replaced run's scan completed")
	assert.Empty(t, rt.marks(t), "a replaced run marks nothing")

	close(second.finish)
	require.NoError(t, <-rt.answer(searchRunRequest("s", "c d", false)))
	assert.Equal(t, []string{searchTab + " quiet=false: Search finished"}, rt.marks(t))

	// New sessions in the person's list start the same query over: the run it replaces marks nothing, and the new
	// one keeps the person's entry and marks once.
	fresh := searchRunRequest("s", "c d", false)
	fresh.Fresh = true

	rt.leave(t, searchRunRequest("s", "c d", true))
	stale := rt.scan(t)
	rt.leave(t, fresh)
	assert.Equal(t, searchEndError("refreshed"), context.Cause(stale.ctx))
	close(stale.finish)
	close(rt.scan(t).finish)
	require.NoError(t, <-rt.answer(searchRunRequest("s", "c d", false)))
	assert.Equal(t, []string{searchTab + " quiet=false: Search finished"}, rt.marks(t))

	// A fresh start of a finished run searches again without marking again.
	rt.leave(t, searchRunRequest("s", "c d", true))
	close(rt.scan(t).finish)
	refreshed := rt.answer(fresh)
	close(rt.scan(t).finish)
	require.NoError(t, <-refreshed)
	assert.Equal(t, []string{searchTab + " quiet=false: Search finished"}, rt.marks(t))

	evicted := rt.answer(searchRunRequest("0", "a b", true))
	oldest := rt.scan(t)

	for i := 1; i < searchRunsKept; i++ {
		rt.leave(t, searchRunRequest(strconv.Itoa(i), "a b", false))
		close(rt.scan(t).finish)
	}

	rt.leave(t, searchRunRequest("ninth", "a b", false))
	ninth := rt.scan(t)

	assert.Equal(t, searchEndError("evicted"), context.Cause(oldest.ctx))

	rt.server.runsMu.Lock()
	assert.Len(t, rt.server.runs["alice@example.com"], searchRunsKept)
	rt.server.runsMu.Unlock()

	close(oldest.finish)
	require.NoError(t, <-evicted, "the evicted run's scan completed")
	assert.Empty(t, rt.marks(t), "an evicted run marks nothing")
	close(ninth.finish)
}

// Stopping the server cancels its running searches, which mark nothing, waits for them, and starts no more.
func TestStopSearchesCancelsAndWaits(t *testing.T) {
	rt := newSearchRunTest(t)

	rt.leave(t, searchRunRequest("s", "a b", true))
	scan := rt.scan(t)

	stopped := make(chan error, 1)

	go func() { stopped <- rt.server.StopSearches() }()

	<-scan.ctx.Done()
	assert.Equal(t, searchEndError("stopped"), context.Cause(scan.ctx))

	select {
	case err := <-stopped:
		t.Fatalf("stopping waits for the running search, got %v", err)
	default:
	}

	close(scan.finish)
	require.NoError(t, <-stopped)
	assert.Empty(t, rt.marks(t), "a stopped search marks nothing")

	_, err := rt.server.searchSessions(rt.alice, searchRunRequest("s", "a b", true))
	assert.Equal(t, codes.Unavailable, status.Code(err))
}
