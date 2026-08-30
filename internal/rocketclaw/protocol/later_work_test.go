package protocol

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMixedLaterWorkDefaultIsQueueThenScheduled(t *testing.T) {
	due := time.Date(2000, 1, 1, 16, 0, 0, 0, time.UTC)
	rows := MixedLaterWork(
		[]ThreadQueueItem{{ID: "q1", Message: "A", Position: 0}, {ID: "q2", Message: "B", Position: 1}},
		map[string]ScheduledMessageState{"s1": {Message: "S", DueAt: due}},
	)
	require.Len(t, rows, 3)
	assert.Equal(t, LaterWorkQueued, rows[0].Kind)
	assert.Equal(t, "q1", rows[0].Queue.ID)
	assert.Equal(t, LaterWorkQueued, rows[1].Kind)
	assert.Equal(t, "q2", rows[1].Queue.ID)
	assert.Equal(t, LaterWorkScheduled, rows[2].Kind)
	assert.Equal(t, "s1", rows[2].ScheduledID)
}

func TestMixedLaterWorkParksAfterScheduled(t *testing.T) {
	due := time.Date(2000, 1, 1, 16, 0, 0, 0, time.UTC)
	rows := MixedLaterWork(
		[]ThreadQueueItem{{ID: "q1", Message: "A", Position: 0, ParkAfter: "s1"}},
		map[string]ScheduledMessageState{"s1": {Message: "S", DueAt: due}},
	)
	require.Len(t, rows, 2)
	assert.Equal(t, LaterWorkScheduled, rows[0].Kind)
	assert.Equal(t, "s1", rows[0].ScheduledID)
	assert.Equal(t, LaterWorkQueued, rows[1].Kind)
	assert.Equal(t, "q1", rows[1].Queue.ID)
}

func TestMixedLaterWorkUnknownParkIsEmpty(t *testing.T) {
	rows := MixedLaterWork(
		[]ThreadQueueItem{{ID: "q1", Message: "A", Position: 0, ParkAfter: "gone"}},
		nil,
	)
	require.Len(t, rows, 1)
	assert.Equal(t, "q1", rows[0].Queue.ID)
}
