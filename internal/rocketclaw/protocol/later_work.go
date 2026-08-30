package protocol

import (
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

// ThreadQueueItem is one persisted Enqueued Slack Message.
type ThreadQueueItem struct {
	ID             string
	ConversationID string
	Message        string
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
	known := make(map[string]struct{}, len(scheduled))
	for id := range scheduled {
		known[id] = struct{}{}
	}

	slots := map[string][]ThreadQueueItem{}

	for i := range queue {
		park := strings.TrimSpace(queue[i].ParkAfter)
		if _, ok := known[park]; park != "" && !ok {
			park = ""
		}

		slots[park] = append(slots[park], queue[i])
	}

	for park, items := range slots {
		slices.SortFunc(items, func(a, b ThreadQueueItem) int {
			if a.Position != b.Position {
				return a.Position - b.Position
			}

			if cmp := a.StashAt.Compare(b.StashAt); cmp != 0 {
				return cmp
			}

			return strings.Compare(a.ID, b.ID)
		})
		slots[park] = items
	}

	pegs := make([]string, 0, len(scheduled))
	for id := range scheduled {
		pegs = append(pegs, id)
	}

	slices.SortFunc(pegs, func(a, b string) int {
		if cmp := scheduled[a].DueAt.Compare(scheduled[b].DueAt); cmp != 0 {
			return cmp
		}

		return strings.Compare(a, b)
	})

	rows := make([]LaterWorkRow, 0, len(queue)+len(scheduled))

	empty := slots[""]
	for i := range empty {
		rows = append(rows, LaterWorkRow{Kind: LaterWorkQueued, Queue: empty[i]})
	}

	for _, id := range pegs {
		rows = append(rows, LaterWorkRow{Kind: LaterWorkScheduled, ScheduledID: id, Scheduled: scheduled[id]})

		parked := slots[id]
		for i := range parked {
			rows = append(rows, LaterWorkRow{Kind: LaterWorkQueued, Queue: parked[i]})
		}
	}

	return rows
}
