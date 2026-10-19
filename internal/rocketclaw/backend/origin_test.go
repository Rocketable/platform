package backend

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOriginJSON(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		origin any
		want   string
	}{
		{"cron", cronOrigin{Kind: originCron, RunKind: cronScheduled}, `{"agent":"","kind":"cron","ranAt":"","runId":"","runKind":"scheduled","sourcePath":"","stem":""}`},
		{"missing pairs", externalMCPOrigin{Kind: originExternalMCP}, `{"agent":"","externalConversationId":"","kind":"external_mcp","pairs":null}`},
		{"empty pairs", externalMCPOrigin{Kind: originExternalMCP, Pairs: []originPair{}}, `{"agent":"","externalConversationId":"","kind":"external_mcp","pairs":[]}`},
		{"caller text", externalMCPOrigin{Kind: originExternalMCP, ExternalConversationID: "id<1>", Pairs: []originPair{{Key: "ticket-id", Value: "<script>"}}}, `{"agent":"","externalConversationId":"id\u003c1\u003e","kind":"external_mcp","pairs":[{"key":"ticket-id","value":"\u003cscript\u003e"}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := json.Marshal(test.origin)
			require.NoError(t, err)
			require.Equal(t, test.want, string(got))
		})
	}
}
