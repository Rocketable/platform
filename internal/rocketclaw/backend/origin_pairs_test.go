package backend

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSidebarChatAllocationBudget(t *testing.T) {
	require.Zero(t, testing.AllocsPerRun(1000, sidebarChatAllocationCheck(t)))
}

func BenchmarkSidebarChat(b *testing.B) {
	check := sidebarChatAllocationCheck(b)
	b.ReportAllocs()

	for b.Loop() {
		check()
	}
}

func sidebarChatAllocationCheck(tb testing.TB) func() {
	tb.Helper()

	cron := &protocol.CronRun{Path: "cron/Weekly report.md", At: time.Date(2001, time.January, 2, 3, 4, 5, 0, time.UTC)}
	binding := ExternalMCPSessionState{ManagedConversationID: "sample-chat", Agent: "helper", OriginPairs: map[string]string{"topic": "Example", "note": "Sample"}}
	cases := []struct {
		facts ChatOriginFacts
		cron  bool
		stem  string
	}{
		{facts: ChatOriginFacts{ConversationID: "sample-chat"}},
		{facts: ChatOriginFacts{ConversationID: "sample-chat", Run: cron}, cron: true, stem: "cron/Weekly report.md"},
		{facts: ChatOriginFacts{ConversationID: "sample-chat", Run: &protocol.CronRun{Path: "cron/.md", OneOff: true}}, cron: true, stem: "cron/.md"},
		{facts: ChatOriginFacts{ConversationID: "sample-chat", Run: &protocol.CronRun{Path: "agents/marketing.md#*/5 * * * *"}}, cron: true, stem: "marketing"},
		{facts: ChatOriginFacts{ConversationID: "sample-chat", Binding: binding}},
		{facts: ChatOriginFacts{ConversationID: "sample-chat", Binding: binding, Run: cron}},
	}

	return func() {
		for i := range cases {
			tc := &cases[i]
			row := SidebarSession{Conversation: protocol.Conversation{ID: "sample-chat"}, Origin: tc.facts}

			chat, err := sidebarChat(tb.Context(), noSlackLookups{}, nil, &row)
			if err != nil || chat.Cron != tc.cron || chat.CronName != tc.stem || chat.Session.Conversation.ID != row.Conversation.ID {
				tb.Fatalf("sidebarChat = %#v, %v; want cron %t, stem %q", chat, err, tc.cron, tc.stem)
			}
		}
	}
}

func TestDecideCronOriginAllocationBudget(t *testing.T) {
	require.LessOrEqual(t, testing.AllocsPerRun(1000, cronOriginAllocationCheck(t)), float64(6))
}

func BenchmarkDecideCronOrigin(b *testing.B) {
	check := cronOriginAllocationCheck(b)
	b.ReportAllocs()

	for b.Loop() {
		check()
	}
}

func cronOriginAllocationCheck(tb testing.TB) func() {
	tb.Helper()

	facts := ChatOriginFacts{ConversationID: "example-chat", CreatingSource: "example-run", ProducerAgent: "Helper Ω",
		Run: &protocol.CronRun{Path: "agents/Weekly %s.md#30m", At: time.Date(2001, time.January, 2, 3, 4, 5, 0, time.UTC)}}

	const want = "cron source: agents/weekly %s.md#30m stem: weekly %s · 30m run kind: scheduled run id: example-run agent: helper ω ran at: 2001-01-02t03:04:05z"

	return func() {
		origin, text := DecideOrigin(&facts)
		if origin.(cronOrigin).Stem != "Weekly %s · 30m" || text != want {
			tb.Fatalf("origin = %#v, text = %q", origin, text)
		}
	}
}

func TestDecideExternalOriginAllocationBudget(t *testing.T) {
	require.LessOrEqual(t, testing.AllocsPerRun(1000, externalOriginAllocationCheck(t)), float64(9))

	facts := ChatOriginFacts{ConversationID: "sample-chat", Binding: ExternalMCPSessionState{ManagedConversationID: "sample-chat", Agent: "helper"}, ExternalConversationID: "sample-external"}
	origin, text := DecideOrigin(&facts)
	require.Equal(t, externalMCPOrigin{Kind: originExternalMCP, ExternalConversationID: "sample-external", Agent: "helper", Pairs: []originPair{}}, origin)
	require.Equal(t, "external mcp external conversation: sample-external agent: helper ", text)
}

func BenchmarkDecideExternalOrigin(b *testing.B) {
	check := externalOriginAllocationCheck(b)
	b.ReportAllocs()

	for b.Loop() {
		check()
	}
}

func externalOriginAllocationCheck(tb testing.TB) func() {
	tb.Helper()

	facts := ChatOriginFacts{ConversationID: "sample-chat", Binding: ExternalMCPSessionState{
		ManagedConversationID: "sample-chat", Agent: "Helper Ω", OriginPairs: map[string]string{"topic": "BILLING", "Path": "/Notes", "empty": "", "": "blank"},
	}, ExternalConversationID: "sample-external"}
	want := []originPair{{Key: "", Value: "blank"}, {Key: "Path", Value: "/Notes"}, {Key: "empty", Value: ""}, {Key: "topic", Value: "BILLING"}}

	const wantText = "external mcp external conversation: sample-external agent: helper ω =blank path=/notes empty= topic=billing"

	return func() {
		origin, text := DecideOrigin(&facts)

		got := origin.(externalMCPOrigin)
		if text != wantText || got.Agent != facts.Binding.Agent || got.ExternalConversationID != facts.ExternalConversationID || got.Kind != originExternalMCP || len(got.Pairs) != len(want) {
			tb.Fatalf("origin = %#v, text = %q", origin, text)
		}

		for i, pair := range got.Pairs {
			if pair != want[i] {
				tb.Fatalf("pair %d = %#v, want %#v", i, pair, want[i])
			}
		}
	}
}

func TestChatOriginFactsAllocationBudget(t *testing.T) {
	if os.Getenv("ROCKETCLAW_TEST_CHAT_ORIGIN_ALLOCATIONS") != "1" {
		cmd := exec.CommandContext(t.Context(), uninstrumentedBackendBinary(t), "-test.run=^"+t.Name()+"$", "-test.cpu=1", "-test.benchtime=32x")

		cmd.Env = append(os.Environ(), "ROCKETCLAW_TEST_CHAT_ORIGIN_ALLOCATIONS=1", "GOGC=100", "GOMEMLIMIT=off")
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)

		return
	}

	var result testing.BenchmarkResult

	synctest.Test(t, func(*testing.T) {
		result = testing.Benchmark(BenchmarkChatOriginFacts)
	})

	require.Positive(t, result.N)
	require.Less(t, result.AllocedBytesPerOp(), int64(7<<20), "benchmark: %s  %s", result, result.MemString())

	if assert.Less(t, result.AllocsPerOp(), int64(752), "benchmark: %s  %s", result, result.MemString()) {
		return
	}

	runtime.GC()
	runtime.GC()

	var records []runtime.MemProfileRecord

	n, ok := runtime.MemProfile(nil, true)
	for !ok {
		records = make([]runtime.MemProfileRecord, n+50)
		n, ok = runtime.MemProfile(records, true)
	}

	records = slices.DeleteFunc(records[:n], func(record runtime.MemProfileRecord) bool {
		return record.AllocObjects == 0 || record.AllocBytes != 8192*record.AllocObjects
	})
	slices.SortFunc(records, func(a, b runtime.MemProfileRecord) int { return cmp.Compare(b.AllocObjects, a.AllocObjects) })

	t.Logf("8 KiB allocation samples by call site (cumulative since process start, including setup; sample rate %d bytes):", runtime.MemProfileRate)

	for i := range min(5, len(records)) {
		record := &records[i]

		var stack []string

		frames := runtime.CallersFrames(record.Stack())
		for range 10 {
			frame, more := frames.Next()
			stack = append(stack, frame.Function)

			if !more {
				break
			}
		}

		t.Logf("%d: %s", record.AllocObjects, strings.Join(stack, " <- "))
	}
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

	var start, end runtime.MemStats
	runtime.ReadMemStats(&start)

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

	runtime.ReadMemStats(&end)
	b.ReportMetric(float64(end.NumGC-start.NumGC), "gc-cycles/run")

	for i, class := range &end.BySize {
		if class.Size == 8192 {
			b.ReportMetric(float64(class.Mallocs-start.BySize[i].Mallocs), "8KiB-class-allocs/run")
			break
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
