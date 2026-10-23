package rocketcode

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTracelessJournal(t *testing.T) {
	errParent := errors.New("journal unavailable")
	for _, tc := range []struct {
		name  string
		found bool
		err   error
	}{
		{name: "found", found: true},
		{name: "missing"},
		{name: "parent failure", found: true, err: errParent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			step := json.RawMessage(`{"revision":2}`)
			items := []json.RawMessage{json.RawMessage(`{"text":"checkpoint"}`)}
			lists := []TurnItems{{List: TurnReplayInput, From: 3, Items: items}}
			parent := &mockJournal{
				LoadFunc: func(gotCtx context.Context, key string) (json.RawMessage, bool, error) {
					require.Equal(t, ctx, gotCtx)
					require.Equal(t, "worker/step", key)

					return step, tc.found, tc.err
				},
				SaveFunc: func(gotCtx context.Context, key string, value json.RawMessage) error {
					require.Equal(t, ctx, gotCtx)
					require.Equal(t, "worker/step", key)
					require.Equal(t, step, value)

					return tc.err
				},
				SaveTurnFunc: func(gotCtx context.Context, turnID string, value json.RawMessage, gotLists []TurnItems) error {
					require.Equal(t, ctx, gotCtx)
					require.Equal(t, "worker", turnID)
					require.Equal(t, step, value)
					require.Equal(t, lists, gotLists)

					return tc.err
				},
				ItemsFunc: func(gotCtx context.Context, turnID string, list TurnList) ([]json.RawMessage, error) {
					require.Equal(t, ctx, gotCtx)
					require.Equal(t, "worker", turnID)
					require.Equal(t, TurnReplayInput, list)

					return items, tc.err
				},
			}
			journal := TracelessJournal{Parent: parent}

			value, found, errLoad := journal.Load(ctx, "worker/step")
			errSave := journal.Save(ctx, "worker/step", step)
			errSaveTurn := journal.SaveTurn(ctx, "worker", step, lists)
			gotItems, errItems := journal.Items(ctx, "worker", TurnReplayInput)

			if tc.err == nil {
				require.NoError(t, errLoad)
				require.NoError(t, errSave)
				require.NoError(t, errSaveTurn)
				require.NoError(t, errItems)
				require.Equal(t, step, value)
				require.Equal(t, tc.found, found)
				require.Equal(t, items, gotItems)
			} else {
				for _, err := range []error{errLoad, errSave, errSaveTurn, errItems} {
					require.ErrorIs(t, err, errParent)
				}

				require.Nil(t, value)
				require.False(t, found)
				require.Nil(t, gotItems)
			}

			require.NoError(t, journal.SaveTrace(ctx, "worker", items))
			require.NoError(t, journal.SaveCodeModeCall(ctx, "worker", "call-1", step))
			calls, err := journal.CodeModeCalls(ctx, "worker")
			require.NoError(t, err)
			require.Empty(t, calls)
			require.Empty(t, parent.SaveTraceCalls())
			require.Empty(t, parent.SaveCodeModeCallCalls())
			require.Empty(t, parent.CodeModeCallsCalls())
		})
	}
}
