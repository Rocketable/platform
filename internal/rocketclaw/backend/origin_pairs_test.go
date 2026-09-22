package backend

import (
	"testing"

	harness "github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/require"
)

func TestOriginPairsTraceDropsInjectedKeys(t *testing.T) {
	trace, err := originPairsTrace(map[string]string{
		"ticket-id":                "123",
		"external_conversation_id": "public-1",
		"rocketclaw_principal":     "alice",
		"note":                     "<script>alert(1)</script>",
	})
	require.NoError(t, err)

	pairs, ok := OriginPairsFromEntry(&harness.SessionEntry{Type: externalMCPOriginPairsEntryType, OutputTrace: trace})
	require.True(t, ok)
	require.Equal(t, map[string]string{"ticket-id": "123", "note": "<script>alert(1)</script>"}, pairs)

	_, ok = OriginPairsFromEntry(&harness.SessionEntry{Type: externalMCPMetadataEntryType, OutputTrace: trace})
	require.False(t, ok)
}
