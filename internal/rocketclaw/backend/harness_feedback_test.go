package backend

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
)

func TestHarnessFeedbackPostsNewIssuesAndThreadsRepeats(t *testing.T) {
	t.Parallel()

	answers := []string{"1", "0", "maybe"}

	var inputs []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input string `json:"input"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		inputs = append(inputs, body.Input)

		answer, _ := json.Marshal(answers[0])
		answers = answers[1:]

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp","object":"response","created_at":0,"status":"completed","model":"m","output":[{"id":"final","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":` + string(answer) + `,"annotations":[]}]}]}`))
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{OpenAI: config.OpenAIConfig{APIKey: "test", APIBaseURL: server.URL}}
	cfg.Slack.FeedbackChannel = "C9"
	cfg.Web.Tsnet = &config.TsnetConfig{Origin: "https://hally.example"}

	type post struct{ channel, thread, text string }

	var posts []post

	threads := newThreadBridgeManager(config.NewLockedConfig(cfg), newTestSessionService(t), slog.New(slog.DiscardHandler), &bridgeFactoryMock{})
	threads.feedback = &harnessFeedbackPosterMock{PostHarnessFeedbackFunc: func(_ context.Context, channel, threadTS, text string) (string, string, error) {
		posts = append(posts, post{channel, threadTS, text})

		return "C9", "100.1", nil
	}}

	tool := harnessFeedbackTool(threads.store, threads, newModelResolver(cfg, slog.New(slog.DiscardHandler)), cfg, "main", "chat")
	report := func(summary string) json.RawMessage {
		raw, _ := json.Marshal(struct {
			Summary string `json:"summary"`
			Details string `json:"details"`
		}{summary, "grep said nothing"})

		return raw
	}

	for _, step := range []struct{ summary, want string }{
		{"grep  hides\nerrors", "Reported as a new harness issue."},
		{"grep is silent on errors", "Added to the known harness issue: grep hides errors."},
		{"read rejects absolute paths", "Reported as a new harness issue."},
	} {
		result, err := tool.Call(t.Context(), report(step.summary), nil)
		require.NoError(t, err)
		assert.Equal(t, step.want, result.Output)
	}

	_, err := tool.Call(t.Context(), report("anything"), nil)
	require.EqualError(t, err, `match harness feedback: model answered "maybe", not an issue number`)

	link := "https://hally.example/s/Y2hhdA"
	assert.Equal(t, []post{
		{"C9", "", "New harness issue: grep hides errors\nAgent main in " + link + "\n\ngrep said nothing"},
		{"C9", "100.1", "Reported again: grep is silent on errors\nAgent main in " + link + "\n\ngrep said nothing"},
		{"C9", "", "New harness issue: read rejects absolute paths\nAgent main in " + link + "\n\ngrep said nothing"},
	}, posts)
	assert.Contains(t, inputs[0], "Known issues:\n1. grep hides errors\n\nNew report:")
	assert.Contains(t, inputs[2], "1. grep hides errors\n2. read rejects absolute paths\n")
	assert.Equal(t, harness.HarnessFeedbackToolName, tool.Name)
}
