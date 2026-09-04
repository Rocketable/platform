package protocol

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWebSessionConversationID(t *testing.T) {
	id := WebSessionConversationID("ops")
	require.Equal(t, "web-session:ops", id)

	name, ok := WebSessionName(id)
	require.True(t, ok)
	require.Equal(t, "ops", name)

	_, ok = WebSessionName(SlackThreadConversationID("C1", "1.2"))
	require.False(t, ok)
}
