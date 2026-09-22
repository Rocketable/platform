package protocol

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"
)

// LaterWorkKind is one row in mixed later-work order.
type LaterWorkKind string

const (
	// LaterWorkQueued is an Enqueued Slack Message row.
	LaterWorkQueued LaterWorkKind = "queued"
	// LaterWorkScheduled is a scheduled-message peg.
	LaterWorkScheduled LaterWorkKind = "scheduled"
)

// ScheduledMessageState records one pending delayed system prompt.
type ScheduledMessageState struct {
	ConversationID string        `json:"conversation_id,omitempty"`
	Agent          string        `json:"agent,omitempty"`
	Message        string        `json:"message,omitempty"`
	DueAt          time.Time     `json:"due_at,omitzero"`
	Recurring      bool          `json:"recurring,omitempty"`
	Interval       time.Duration `json:"interval,omitempty"`
}

// ThreadQueueItem is one persisted waiting message. A zero Kind means enqueue.
type ThreadQueueItem struct {
	Kind           InboundKind
	ID             string
	ConversationID string
	Message        string
	Content        InboundContent
	Source         Source
	SlackReply     *SlackReplyTarget
	Principal      string
	StashAt        time.Time
	Position       int
	ParkAfter      string
	SlackChannel   string
	SlackTS        string
}

// LaterWorkRow is one mixed-list entry.
type LaterWorkRow struct {
	Kind        LaterWorkKind
	Queue       ThreadQueueItem
	ScheduledID string
	Scheduled   ScheduledMessageState
}

// MixedLaterWork builds later-work order: empty-park enqueue, then each scheduled peg
// in due-time order with enqueue parked after that peg. Unknown park-after is empty.
func MixedLaterWork(queue []ThreadQueueItem, scheduled map[string]ScheduledMessageState) []LaterWorkRow {
	slots := map[string][]ThreadQueueItem{}

	for i := range queue {
		if queue[i].Kind == InboundKindHeld {
			continue
		}

		park := strings.TrimSpace(queue[i].ParkAfter)
		if _, ok := scheduled[park]; park != "" && !ok {
			park = ""
		}

		slots[park] = append(slots[park], queue[i])
	}

	for _, items := range slots {
		slices.SortFunc(items, func(a, b ThreadQueueItem) int {
			return cmp.Or(cmp.Compare(a.Position, b.Position), a.StashAt.Compare(b.StashAt), strings.Compare(a.ID, b.ID))
		})
	}

	rows := make([]LaterWorkRow, 0, len(queue)+len(scheduled))

	for i := range slots[""] {
		rows = append(rows, LaterWorkRow{Kind: LaterWorkQueued, Queue: slots[""][i]})
	}

	for _, id := range slices.SortedFunc(maps.Keys(scheduled), func(a, b string) int {
		return cmp.Or(scheduled[a].DueAt.Compare(scheduled[b].DueAt), strings.Compare(a, b))
	}) {
		rows = append(rows, LaterWorkRow{Kind: LaterWorkScheduled, ScheduledID: id, Scheduled: scheduled[id]})

		for i := range slots[id] {
			rows = append(rows, LaterWorkRow{Kind: LaterWorkQueued, Queue: slots[id][i]})
		}
	}

	return rows
}
