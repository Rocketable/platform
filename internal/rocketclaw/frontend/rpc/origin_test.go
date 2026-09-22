package rpc

import (
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/stretchr/testify/require"
)

func TestParseCronRun(t *testing.T) {
	scheduled := "cron:cron/inbox-delta-monitor.md:20260902T030027.554785000Z:GV4ST"
	run, ok := parseCronRun(scheduled)
	require.True(t, ok)
	require.Equal(t, cronScheduled, run.kind)
	require.Equal(t, "cron/inbox-delta-monitor.md", run.path)
	require.Equal(t, "inbox-delta-monitor", run.stem)
	require.Equal(t, "2026-09-02T03:00:27.554785Z", run.at.Format(time.RFC3339Nano))

	oneOff := "one-off-cron:cron/report_daily.md:20260905T020000.000000002Z:second"
	run, ok = parseCronRun(oneOff)
	require.True(t, ok)
	require.Equal(t, cronOneOff, run.kind)
	require.Equal(t, "report_daily", run.stem)

	_, ok = parseCronRun("slack-thread:C1:1")
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
			got, err := originJSON(test.origin)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestCreatingCronLocatorIgnoresLaterOneOffOnSlackThread(t *testing.T) {
	later := "one-off-cron:cron/daily.md:20260905T020000.000000002Z:second"
	entries := []backend.ObservedSessionEntry{{SourceConversationID: later}}
	locator, ok := creatingCronLocator("slack-thread:C1:1.2", "", true, entries)
	require.False(t, ok)
	require.Empty(t, locator)

	locator, ok = creatingCronLocator("slack-thread:C1:1.2", backend.ThreadCreatedByCron, true, entries)
	require.True(t, ok)
	require.Equal(t, later, locator)

	webSource := "web:" + later
	locator, ok = creatingCronLocator(webSource, "", false, nil)
	require.True(t, ok)
	require.Equal(t, later, locator)
}
