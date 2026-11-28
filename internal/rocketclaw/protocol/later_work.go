package protocol

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"
)

type LaterWorkKind string

const (
	LaterWorkQueued    LaterWorkKind = "queued"
	LaterWorkScheduled LaterWorkKind = "scheduled"
)

type ScheduledMessageState struct {
	ConversationID string        `json:"conversation_id,omitempty"`
	Agent          string        `json:"agent,omitempty"`
	Message        string        `json:"message,omitempty"`
	DueAt          time.Time     `json:"due_at,omitzero"`
	Recurring      bool          `json:"recurring,omitempty"`
	Interval       time.Duration `json:"interval,omitempty"`
	Origin         string        `json:"origin,omitempty"`
}

type ThreadQueueItem struct {
	Kind           InboundKind
	ID             string
	ConversationID string
	StashAt        time.Time
	Position       int
	ParkAfter      string
	Inbound        *InboundMessage
}

type LaterWorkRow struct {
	Kind        LaterWorkKind
	Queue       ThreadQueueItem
	ScheduledID string
	Scheduled   ScheduledMessageState
}

func MixedLaterWork(queue []ThreadQueueItem, scheduled map[string]ScheduledMessageState) []LaterWorkRow {
	slots := map[string][]int{}

	for i := range queue {
		if queue[i].Kind == InboundKindHeld {
			continue
		}

		park := strings.TrimSpace(queue[i].ParkAfter)
		if _, ok := scheduled[park]; park != "" && !ok {
			park = ""
		}

		slots[park] = append(slots[park], i)
	}

	for _, indices := range slots {
		slices.SortFunc(indices, func(i, j int) int {
			a, b := &queue[i], &queue[j]
			return cmp.Or(cmp.Compare(a.Position, b.Position), a.StashAt.Compare(b.StashAt), strings.Compare(a.ID, b.ID))
		})
	}

	rows := make([]LaterWorkRow, 0, len(queue)+len(scheduled))

	for _, i := range slots[""] {
		rows = append(rows, LaterWorkRow{Kind: LaterWorkQueued, Queue: queue[i]})
	}

	for _, id := range slices.SortedFunc(maps.Keys(scheduled), func(a, b string) int {
		return cmp.Or(scheduled[a].DueAt.Compare(scheduled[b].DueAt), strings.Compare(a, b))
	}) {
		rows = append(rows, LaterWorkRow{Kind: LaterWorkScheduled, ScheduledID: id, Scheduled: scheduled[id]})

		for _, i := range slots[id] {
			rows = append(rows, LaterWorkRow{Kind: LaterWorkQueued, Queue: queue[i]})
		}
	}

	return rows
}
