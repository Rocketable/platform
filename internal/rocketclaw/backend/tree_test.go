package backend

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/require"
)

func TestMessageTreeListsEveryBranch(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	recordTestConversations(t, s, "main", map[string]ConversationKind{"chat": KindWebChat, "run": KindExternalMCPRun})

	appendEntry := func(items ...string) int64 {
		id, err := s.AppendEntryID(ctx, "chat", testReplayEntry(items...))
		require.NoError(t, err)

		return id
	}
	user := func(text string) string { return `{"type":"message","role":"user","content":` + jsonString(text) + `}` }
	assistant := func(text string) string {
		return `{"type":"message","role":"assistant","content":` + jsonString(text) + `}`
	}
	id := func(entry int64, index int) string { return fmt.Sprintf("%d:%d", entry, index) }

	a := appendEntry(user("A\n\n  first"), assistant(strings.Repeat("long ", 40)))
	b := appendEntry(user("B"), `{"type":"function_call","call_id":"call-1","name":"look","arguments":"{}"}`, `{"type":"function_call_output","call_id":"call-1","output":"seen"}`, assistant("b"))
	c := appendEntry(user("C"))
	moveBefore(t, s, "chat", c, 0)

	other := appendEntry(user("C'"), assistant("c'"))
	_, _, err := stageRevertDB(ctx, s.db, "chat", id(b, 0), protocol.RevertThrough)
	require.NoError(t, err)

	d := appendEntry(user("D"), assistant("d"))

	type row struct {
		entry         int64
		index         int
		parent, text  string
		branch, flags string
	}

	tree := func(rows ...row) []MessageTreeNode {
		t.Helper()

		nodes := make([]MessageTreeNode, len(rows))
		for i, r := range rows {
			role := "user"
			if strings.Contains(r.flags, "a") {
				role = "assistant"
			}

			nodes[i] = MessageTreeNode{ID: id(r.entry, r.index), Parent: r.parent, Role: role, Text: r.text, Branch: r.branch, entry: r.entry, index: r.index,
				Outside: strings.Contains(r.flags, "x"), Pickable: strings.Contains(r.flags, "p"), Through: strings.Contains(r.flags, "t"),
				OnPath: strings.Contains(r.flags, "o"), Current: strings.Contains(r.flags, "c"), Deletable: strings.Contains(r.flags, "d")}
		}

		return nodes
	}

	nodes, err := s.MessageTree(ctx, "chat")
	require.NoError(t, err)
	require.Equal(t, tree(
		row{a, 0, "", "A first", "", "po"},
		row{a, 1, id(a, 0), strings.Repeat("long ", 23) + "long…", "", "apto"},
		row{b, 0, id(a, 1), "B", "", "po"},
		row{b, 3, id(b, 0), "b", id(b, 1), "aptd"},
		row{c, 0, id(b, 3), "C", id(c, 0), "ptd"},
		row{other, 0, id(b, 3), "C'", id(other, 0), "pd"},
		row{other, 1, id(other, 0), "c'", "", "apt"},
		row{d, 0, id(b, 0), "D", id(d, 0), "pod"},
		row{d, 1, id(d, 0), "d", "", "aptoc"},
	), nodes)

	finishTestRunTurn(t, s, "run", "chat", "run-1", testSessionEntry("report", "done"))

	var report int64
	require.NoError(t, queryScalar(ctx, s.db, "report entry", `SELECT id FROM entries WHERE conversation_id = 'run'`, &report))

	nodes, err = s.MessageTree(ctx, "chat")
	require.NoError(t, err)
	require.Equal(t, tree(
		row{a, 0, "", "A first", "", "o"},
		row{a, 1, id(a, 0), strings.Repeat("long ", 23) + "long…", "", "ato"},
		row{b, 0, id(a, 1), "B", "", "o"},
		row{b, 3, id(b, 0), "b", id(b, 1), "atd"},
		row{c, 0, id(b, 3), "C", id(c, 0), "td"},
		row{other, 0, id(b, 3), "C'", id(other, 0), "d"},
		row{other, 1, id(other, 0), "c'", "", "at"},
		row{d, 0, id(b, 0), "D", id(d, 0), "o"},
		row{d, 1, id(d, 0), "d", "", "ato"},
		row{report, 0, id(d, 1), "report", "", "xo"},
		row{report, 1, id(report, 0), "done", "", "axptoc"},
	), nodes, "nothing before the report can be picked, and the branch holding it cannot be deleted")

	for i := range nodes {
		if branch := nodes[i].Branch; branch != "" {
			_, err := branchTestRuntime(s).DeleteBranch(ctx, "chat", branch, true)
			require.NoError(t, err, "branch delete takes %s", branch)
		}
	}
}
