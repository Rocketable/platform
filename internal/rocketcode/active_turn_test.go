package rocketcode

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestTurnObservationsPersistPublicProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		journal := recordingJournal()
		owner := turnObservations{journal: journal, turnID: "turn-1"}

		var workers errgroup.Group
		for _, id := range []string{"item-a", "item-b"} {
			workers.Go(func() error {
				return owner.observe(t.Context(), &PublicProgress{ID: id, Kind: PublicProgressText, State: PublicProgressWorking, Text: id, Agent: "main", Model: "model"})
			})
		}

		synctest.Wait()
		require.NoError(t, workers.Wait())
		require.Len(t, journal.SaveTraceCalls(), 2)
		require.Equal(t, "turn-1", journal.SaveTraceCalls()[1].TurnID)
		require.Len(t, PublicProgressFromTrace(journal.SaveTraceCalls()[1].Trace), 2, "concurrent workers both land in the trace")

		require.NoError(t, owner.observe(t.Context(), &PublicProgress{ID: "item-a", Kind: PublicProgressText, State: PublicProgressWorking, Text: "item-a", Agent: "main", Model: "model"}))
		require.Len(t, journal.SaveTraceCalls(), 2, "identical public state must not write")

		replacement := PublicProgress{ID: "item-a", Kind: PublicProgressText, State: PublicProgressCompleted, Agent: "main", Model: "model"}
		require.NoError(t, owner.observe(t.Context(), &replacement))
		require.Contains(t, PublicProgressFromTrace(journal.SaveTraceCalls()[2].Trace), replacement, "empty text replaces rather than appends to the previous text")

		replacement.Agent = ""
		require.NoError(t, owner.finishCall(t.Context(), &replacement, PublicProgressCompleted))
		require.Empty(t, replacement.Agent, "completion must not mutate the worker's snapshot")
		require.Equal(t, "main", PublicProgressFromTrace(journal.SaveTraceCalls()[2].Trace)[0].Agent, "later caller mutations must not alter stored attribution")

		require.NoError(t, owner.replaceResponse(t.Context(), "", nil))
		require.Empty(t, PublicProgressFromTrace(journal.SaveTraceCalls()[3].Trace), "removing the attempt drops its text")

		owner.close()
		require.NoError(t, owner.observe(t.Context(), &PublicProgress{ID: "late", Kind: PublicProgressText, State: PublicProgressWorking, Text: "too late"}))
		require.Len(t, journal.SaveTraceCalls(), 4, "a closed turn ignores late workers")
	})
}

func TestTurnObservationPersistenceFailure(t *testing.T) {
	errPersist := errors.New("storage unavailable")
	journal := recordingJournal()
	journal.SaveTraceFunc = func(context.Context, string, []json.RawMessage) error { return errPersist }
	owner := turnObservations{journal: journal, turnID: "turn-1"}

	progress := PublicProgress{ID: "item-1", Kind: PublicProgressText, State: PublicProgressWorking, Text: "hello"}
	err := owner.observe(t.Context(), &progress)
	require.ErrorIs(t, err, errPersist)
	_, ok := errors.AsType[progressPersistenceError](err)
	require.True(t, ok, "persistence failure must be distinguishable from provider/tool errors")
	require.ErrorIs(t, owner.observe(t.Context(), &progress), errPersist)

	owner.close()
	require.NoError(t, owner.observe(t.Context(), &progress), "closed lifetime ignores late workers even after a write failure")

	owner = turnObservations{journal: journal, turnID: "turn-2"}
	err = owner.replaceResponse(t.Context(), "", []PublicProgress{progress})
	require.ErrorIs(t, err, errPersist)
	_, ok = errors.AsType[progressPersistenceError](err)
	require.True(t, ok)
	require.Empty(t, owner.trace, "a failed save must not commit the replacement in memory")
	require.ErrorIs(t, owner.replaceResponse(t.Context(), "", []PublicProgress{progress}), errPersist)
	require.Len(t, journal.SaveTraceCalls(), 2)
	require.Same(t, t.Context(), journal.SaveTraceCalls()[1].Ctx)
	require.Equal(t, "turn-2", journal.SaveTraceCalls()[1].TurnID)
	require.Equal(t, []PublicProgress{progress}, PublicProgressFromTrace(journal.SaveTraceCalls()[1].Trace))
}

func TestPublicProgressTraceAllowlist(t *testing.T) {
	traces := []json.RawMessage{
		json.RawMessage(`{"type":"reasoning","text":"PRIVATE"}`),
		json.RawMessage(`{"type":"unknown","text":"PRIVATE"}`),
		json.RawMessage(`{"type":"rocketcode_public_progress","progress":{"id":"unknown-kind","kind":"reasoning","state":"working","text":"PRIVATE"}}`),
		json.RawMessage(`{"type":"rocketcode_public_progress","progress":{"id":"unknown-state","kind":"text","state":"private","text":"PRIVATE"}}`),
		json.RawMessage(`{"type":"rocketcode_public_progress","progress":{"id":"item-1","kind":"text","state":"working","text":"hello","agent":"main","model":"model"}}`),
	}
	require.Equal(t, []PublicProgress{{ID: "item-1", Kind: PublicProgressText, State: PublicProgressWorking, Text: "hello", Agent: "main", Model: "model"}}, PublicProgressFromTrace(traces))

	journal := recordingJournal()
	owner := turnObservations{journal: journal, turnID: "resumed", trace: traces}
	require.NoError(t, owner.observe(t.Context(), new(PublicProgressFromTrace(traces)[0])))
	require.Empty(t, journal.SaveTraceCalls(), "a restored trace recognizes equivalent public JSON without rewriting it")
}
