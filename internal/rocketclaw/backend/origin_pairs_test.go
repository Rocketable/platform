package backend

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/require"
)

func TestDecideCronOriginAllocationBudget(t *testing.T) {
	result := testing.Benchmark(BenchmarkDecideCronOrigin)
	require.Positive(t, result.N)
	require.LessOrEqual(t, result.AllocsPerOp(), int64(6))
}

func BenchmarkDecideCronOrigin(b *testing.B) {
	facts := ChatOriginFacts{ConversationID: "example-chat", CreatingSource: "example-run", ProducerAgent: "Helper Ω",
		Run: &protocol.CronRun{Path: "cron/Weekly %s.md", At: time.Date(2001, time.January, 2, 3, 4, 5, 0, time.UTC)}}

	const want = "cron source: cron/weekly %s.md stem: weekly %s run kind: scheduled run id: example-run agent: helper ω ran at: 2001-01-02t03:04:05z"

	b.ReportAllocs()

	for b.Loop() {
		origin, text := DecideOrigin(&facts)
		if origin.(cronOrigin).Stem != "Weekly %s" || text != want {
			b.Fatalf("origin = %#v, text = %q", origin, text)
		}
	}
}

func TestChatOriginFactsAllocationBudget(t *testing.T) {
	result := testing.Benchmark(BenchmarkChatOriginFacts)
	require.Positive(t, result.N)
	// Decoding in place costs up to 6.0 MiB under CI's -race; copying first cost 7.4 MiB, or 8.4 MiB under -race.
	require.Less(t, result.AllocedBytesPerOp(), int64(7<<20))
}

func BenchmarkChatOriginFacts(b *testing.B) {
	store, err := NewSessionService(b.TempDir())
	require.NoError(b, err)
	b.Cleanup(func() { require.NoError(b, store.Stop()) })

	values := make(map[string]string, 32)

	for i := range 32 {
		id := fmt.Sprintf("origin-%02d", i)
		values[id] = strings.Repeat(id, 8192)
		binding := ExternalMCPSessionState{PrivateConversationID: "private-" + id, ManagedConversationID: id, Agent: "helper", OriginPairs: map[string]string{"example": values[id]}}
		require.NoError(b, store.RegisterExternalMCPConversation("external-"+id, "helper", &binding))
	}

	b.ReportAllocs()

	for b.Loop() {
		var rows []ChatOriginFacts

		for facts, err := range store.ChatOriginFacts(b.Context(), "") {
			require.NoError(b, err)

			rows = append(rows, facts)
		}

		require.Len(b, rows, len(values))

		for i := range rows {
			facts := &rows[i]
			require.Equal(b, map[string]string{"example": values[facts.ConversationID]}, facts.Binding.OriginPairs)
		}
	}
}

func TestExternalMCPOriginDetailsStayOutsideHistory(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	binding := ExternalMCPSessionState{PrivateConversationID: "private", ManagedConversationID: "managed", Agent: "helper", OriginPairs: map[string]string{"topic": "billing"}}
	require.NoError(t, store.RegisterExternalMCPConversation("external", "helper", &binding))

	for _, id := range []string{"private", "managed"} {
		_, got, found, err := store.ExternalMCPSessionByConversationID(id)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, binding, got)
		entries, err := store.ObserveEntries(t.Context(), id)
		require.NoError(t, err)
		require.Empty(t, entries)
	}
}

func TestChatOriginFactsReportsFailures(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()

	for _, id := range []string{"first", "second"} {
		require.NoError(t, store.UpsertThread(id, ThreadState{Agent: "main"}))
	}

	seen := 0

	for _, err := range store.ChatOriginFacts(ctx, "") {
		require.NoError(t, err)

		seen++

		break
	}

	require.Equal(t, 1, seen, "stopping early ends the scan")

	_, err := store.db.ExecContext(ctx, `UPDATE conversations SET external_id = 'external', external_run = 'private', external_agent = 'helper',
    origin_pairs = '"not an object"' WHERE conversation_id = 'first'`)
	require.NoError(t, err)

	var errs []error

	for _, err := range store.ChatOriginFacts(ctx, "") {
		if err != nil {
			errs = append(errs, err)
		}
	}

	require.Len(t, errs, 1)
	require.ErrorContains(t, errs[0], "decode external MCP origin pairs")
	// A single-session origin read must neither visit nor decode unrelated bindings.
	seen = 0

	for facts, err := range store.ChatOriginFacts(ctx, "second") {
		require.NoError(t, err)
		require.Equal(t, "second", facts.ConversationID)

		seen++
	}

	require.Equal(t, 1, seen)

	for _, err := range store.ChatOriginFacts(ctx, "missing") {
		require.NoError(t, err)
		t.Fatal("missing origin unexpectedly returned a row")
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()

	errs = nil

	for _, err := range store.ChatOriginFacts(canceled, "") {
		errs = append(errs, err)
	}

	require.Len(t, errs, 1)
	require.ErrorIs(t, errs[0], context.Canceled)
}
