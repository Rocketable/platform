package backend

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseCronRun(t *testing.T) {
	scheduled := "cron:cron/inbox-delta-monitor.md:20260902T030027.554785000Z:GV4ST"
	run, ok := ParseCronRun(scheduled)
	require.True(t, ok)
	require.Equal(t, cronScheduled, run.kind)
	require.Equal(t, "cron/inbox-delta-monitor.md", run.path)
	require.Equal(t, "inbox-delta-monitor", run.Stem)
	require.Equal(t, "2026-09-02T03:00:27.554785Z", run.At.Format(time.RFC3339Nano))

	oneOff := "one-off-cron:cron/report_daily.md:20260905T020000.000000002Z:second"
	run, ok = ParseCronRun(oneOff)
	require.True(t, ok)
	require.Equal(t, cronOneOff, run.kind)
	require.Equal(t, "report_daily", run.Stem)

	_, ok = ParseCronRun("slack-thread:C1:1")
	require.False(t, ok)
}

func TestOriginJSON(t *testing.T) {
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

func TestCreatingCronLocator(t *testing.T) {
	run := "one-off-cron:cron/daily.md:20260905T020000.000000002Z:second"

	for _, test := range []struct {
		name, id, source string
		createdBy        ThreadCreator
		want             string
	}{
		{"slack thread not created by cron", "slack-thread:C1:1.2", run, "", ""},
		{"slack thread created by cron", "slack-thread:C1:1.2", run, ThreadCreatedByCron, run},
		{"web chat opened from cron", "web:" + run, "", "", run},
		{"web chat created by cron entry", "web-chat", run, "alice", run},
		{"web chat created by a person", "web-chat", "", "alice", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			locator, ok := creatingCronLocator(test.id, test.createdBy, test.source)
			require.Equal(t, test.want != "", ok)
			require.Equal(t, test.want, locator)
		})
	}
}
