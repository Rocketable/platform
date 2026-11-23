package backend

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
)

type MessageTreeNode struct {
	ID, Parent, Role, Text, Branch                         string
	Outside, Pickable, Through, OnPath, Current, Deletable bool
	entry                                                  int64
	index                                                  int
}

type treePoint struct {
	entry  int64
	length int
}

const outsideItemSQL = `(e.synced OR item->>'role' = 'user' AND (item->>'prompt_header' !~ '^\[(Web|System)[ \]]' OR item->>'prompt_header' IS NULL AND m.kind = 'slack_thread'))`

const messageTreeSQL = `, chat AS (
    SELECT kind FROM conversations WHERE conversation_id = $1
), linked AS (
    SELECT l.entry_id AS id, e.synced, l.seq, l.parent_entry_id, l.parent_length,
        CASE WHEN jsonb_typeof(e.entry->'replay_input') = 'array' THEN e.entry->'replay_input' ELSE '[]' END AS items
    FROM history_links l LEFT JOIN physical_entries e ON e.id = l.entry_id WHERE l.conversation_id = $1
), tree AS (
    SELECT t.id, t.synced, t.seq, t.items, COALESCE(t.parent_entry_id, 0) AS parent, COALESCE(t.parent_length, jsonb_array_length(p.items), 0) AS parent_length
    FROM linked t LEFT JOIN linked p ON p.id = t.parent_entry_id
), messages AS (
    SELECT e.id, i.ordinal - 1 AS idx, i.item::text AS raw, COALESCE(` + outsideItemSQL + `, FALSE) AS outside
    FROM tree e CROSS JOIN chat m CROSS JOIN LATERAL jsonb_array_elements(e.items) WITH ORDINALITY AS i(item, ordinal)
    WHERE item->>'role' IN ('user', 'assistant')
)
SELECT t.id, t.parent, t.parent_length, jsonb_array_length(t.items), COALESCE(m.idx, -1), COALESCE(m.raw, ''), COALESCE(m.outside, FALSE),
    COALESCE(f.entry_id, 0), COALESCE(f.length, -1)
FROM tree t LEFT JOIN messages m USING (id) CROSS JOIN leaf f ORDER BY t.seq, m.idx`

type treeEntry struct {
	parent treePoint
	length int
	nodes  []int
}

type messageTree struct {
	nodes   []MessageTreeNode
	entries map[int64]*treeEntry
	links   map[treePoint]int
}

func (t *messageTree) contains(at treePoint, entry int64, index int) bool {
	for e, ok := t.entries[at.entry]; ok; e, ok = t.entries[at.entry] {
		if at.entry == entry {
			return index < at.length
		}

		at = e.parent
	}

	return false
}

func (t *messageTree) last(at treePoint) int {
	for e, ok := t.entries[at.entry]; ok; e, ok = t.entries[at.entry] {
		for _, n := range slices.Backward(e.nodes) {
			if t.nodes[n].index < at.length {
				return n
			}
		}

		at = e.parent
	}

	return -1
}

func (t *messageTree) branch(n *MessageTreeNode, parent int) string {
	at := treePoint{n.entry, n.index}
	for e, ok := t.entries[at.entry]; ok; e, ok = t.entries[at.entry] {
		inside := parent >= 0 && t.nodes[parent].entry == at.entry

		stop := 1
		if inside {
			stop = t.nodes[parent].index + 1
		}

		for ; at.length >= stop; at.length-- {
			if t.links[at] > 0 {
				return fmt.Sprintf("%d:%d", at.entry, at.length)
			}
		}

		up, ok := t.entries[e.parent.entry]
		if inside || !ok {
			return ""
		}

		if alternatives := t.links[e.parent]; alternatives > 1 || alternatives == 1 && e.parent.length < up.length {
			return fmt.Sprintf("%d:0", at.entry)
		}

		at = treePoint{e.parent.entry, e.parent.length - 1}
	}

	return ""
}

func (s *SessionService) MessageTree(ctx context.Context, conversationID string) ([]MessageTreeNode, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin message tree: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	_, eligible, _, floor, err := revertStateDB(ctx, tx, conversationID)
	if err != nil {
		return nil, err
	}

	var leaf treePoint

	tree := messageTree{entries: make(map[int64]*treeEntry), links: make(map[treePoint]int)}

	err = readRows(ctx, tx, `WITH `+sessionHistorySQL+messageTreeSQL, "message tree", func(row rowScanner) error {
		var (
			id      int64
			entry   treeEntry
			index   int
			raw     string
			outside bool
		)
		if err := row.Scan(&id, &entry.parent.entry, &entry.parent.length, &entry.length, &index, &raw, &outside, &leaf.entry, &leaf.length); err != nil {
			return fmt.Errorf("scan message tree: %w", err)
		}

		if tree.entries[id] == nil {
			tree.entries[id] = &entry
			tree.links[entry.parent]++
		}

		if index < 0 {
			return nil
		}

		items, err := harness.ReplayInputToParams([]json.RawMessage{json.RawMessage(raw)})
		if err != nil {
			return fmt.Errorf("decode message tree item: %w", err)
		}

		role, text, ok, err := ReplayInputMessageRoleText(&items[0], json.RawMessage(raw))
		if err != nil || !ok {
			return err
		}

		text, _, _ = strings.Cut(text, "\n\nattachment:")

		text = strings.Join(strings.Fields(text), " ")
		if r := []rune(text); len(r) > 120 {
			text = string(r[:119]) + "…"
		}

		tree.entries[id].nodes = append(tree.entries[id].nodes, len(tree.nodes))
		tree.nodes = append(tree.nodes, MessageTreeNode{ID: fmt.Sprintf("%d:%d", id, index), Role: role, Text: text, Outside: outside, entry: id, index: index})

		return nil
	}, conversationID)
	if err != nil {
		return nil, err
	}

	if e, ok := tree.entries[leaf.entry]; ok && leaf.length < 0 {
		leaf.length = e.length
	}

	floorEntry, floorIndex, _ := parseMessageID(floor)
	floored := floor != ""
	nodes := tree.nodes
	parents, children := make([]int, len(nodes)), make([]int, len(nodes))

	path := make(map[int64]int)
	for at, e := leaf, tree.entries[leaf.entry]; e != nil; at, e = e.parent, tree.entries[e.parent.entry] {
		path[at.entry] = at.length
	}

	for i := range nodes {
		n := &nodes[i]
		if parents[i] = tree.last(treePoint{n.entry, n.index}); parents[i] >= 0 {
			n.Parent = nodes[parents[i]].ID
			children[parents[i]]++
		}

		length, ok := path[n.entry]
		n.OnPath = ok && n.index < length
	}

	if current := tree.last(leaf); current >= 0 {
		nodes[current].Current = true
	}

	for i := range nodes {
		n := &nodes[i]
		n.Through = n.Role == "assistant" || children[i] == 0 && !n.OnPath

		end := n.index
		if n.Through {
			end++
		}

		n.Pickable = eligible && (!floored || tree.contains(treePoint{n.entry, end}, floorEntry, floorIndex))

		n.Branch = tree.branch(n, parents[i])
		if n.Branch == "" {
			continue
		}

		first, index, _ := parseMessageID(n.Branch)
		n.Deletable = eligible && (!floored || !tree.contains(treePoint{floorEntry, floorIndex + 1}, first, index))
	}

	return nodes, nil
}
