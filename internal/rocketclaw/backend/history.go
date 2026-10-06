package backend

import (
	"context"
	"database/sql"
	"fmt"
)

// HistoryInventory follows a stateless saved-entry view, not an event cursor.
type HistoryInventory struct {
	ConversationID string            `json:"conversation_id"`
	Source         string            `json:"source"`
	Entries        map[string]string `json:"entries"`
	From           int64             `json:"from,omitempty"`
	Oldest         int64             `json:"oldest,omitempty"`
	Marker         string            `json:"revert_message_id,omitempty"`
}

// HistorySnapshot keeps cutoff, page bounds, payloads and delegation links coherent.
type HistorySnapshot struct {
	Inventory   HistoryInventory
	Entries     []ObservedSessionEntry
	Delegations []string
	Eligible    bool
	Predecessor string
	Reset       bool
}

// ObserveHistory reads every part of a followed view from one database snapshot.
func (s *SessionService) ObserveHistory(ctx context.Context, id, source string, from, before int64, limit int, previous HistoryInventory) (view HistorySnapshot, err error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return view, fmt.Errorf("begin history snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	marker, eligible, predecessor, err := revertStateDB(ctx, tx, id)
	if err != nil {
		return view, err
	}

	if previous.ConversationID != id || previous.Source != source || previous.Marker != marker {
		previous = HistoryInventory{}
	}

	var oldest int64

	if limit > 0 || before > 0 {
		var start int64
		if err := tx.QueryRowContext(ctx, transcriptPageSQL, id, before, max(limit, 1)-1).Scan(&start, &oldest); err != nil {
			return view, fmt.Errorf("read history page: %w", err)
		}

		switch {
		case limit == 0:
		case before == 0 && previous.Entries != nil && previous.Oldest == oldest:
			// Ponytail: follow the open range; slide it forward if long-lived tabs grow too large.
			from = previous.From
		default:
			from = start
		}
	}

	if before > 0 || previous.Oldest != oldest {
		previous = HistoryInventory{}
	}

	view = HistorySnapshot{Inventory: HistoryInventory{ConversationID: id, Source: source, From: from, Oldest: oldest, Marker: marker}, Eligible: eligible, Predecessor: predecessor, Reset: previous.Entries == nil}

	view.Entries, err = observeTranscriptDB(ctx, tx, id, from, before, previous.Entries)
	if err != nil {
		return view, err
	}

	view.Delegations, err = queryStrings(ctx, tx, delegationsSQL, "history delegations", id, source, from, before)
	if err != nil {
		return view, err
	}

	if err := tx.Commit(); err != nil {
		return view, fmt.Errorf("commit history snapshot: %w", err)
	}

	return view, nil
}
