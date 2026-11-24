package harnessfeedback

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReportPostsNewIssuesAndThreadsRepeats(t *testing.T) {
	t.Parallel()

	answers := []string{"1", "0", "maybe"}

	var prompts []string

	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)
		assert.Equal(t, "grouper", body.Model)
		prompts = append(prompts, body.Messages[1].Content)

		answer, _ := json.Marshal(answers[0])
		answers = answers[1:]

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","created":0,"model":"grouper","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":` + string(answer) + `}}]}`))
	}))
	t.Cleanup(model.Close)

	type post struct{ channel, thread, text string }

	var posts []post

	slackAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NoError(t, r.ParseForm())
		assert.Equal(t, "/chat.postMessage", r.URL.Path)
		posts = append(posts, post{r.Form.Get("channel"), r.Form.Get("thread_ts"), r.Form.Get("text")})

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C9","ts":"100.` + strconv.Itoa(len(posts)) + `"}`))
	}))
	t.Cleanup(slackAPI.Close)

	db, err := openIssues(t.Context(), filepath.Join(t.TempDir(), "issues.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.ExecContext(t.Context(), `INSERT INTO issues (summary, channel, ts, reported_at) VALUES ('old problem', 'C9', '1.1', ?)`, time.Now().Add(-15*24*time.Hour).Unix())
	require.NoError(t, err)

	r := reporter{
		slack:     slack.New("xoxb-test", slack.OptionAPIURL(slackAPI.URL+"/")),
		model:     openai.NewClient(option.WithBaseURL(model.URL+"/v1"), option.WithAPIKey("key")),
		modelName: "grouper",
		channel:   "#harness",
		db:        db,
		session:   "https://rocketclaw.example/s/Y2hhdA",
	}

	serverEnd, clientEnd := mcp.NewInMemoryTransports()
	_, err = r.server().Connect(t.Context(), serverEnd, nil)
	require.NoError(t, err)

	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), clientEnd, nil)
	require.NoError(t, err)

	call := func(summary string) (string, bool) {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "report", Arguments: reportInput{Summary: summary, Details: "grep said nothing"}})
		require.NoError(t, err)

		return result.Content[0].(*mcp.TextContent).Text, result.IsError
	}

	for _, step := range []struct{ summary, want string }{
		{"grep  hides\nerrors", "Reported as a new harness issue."},
		{"grep is silent on errors", "Added to the known harness issue: grep hides errors."},
		{"read rejects absolute paths", "Reported as a new harness issue."},
	} {
		text, failed := call(step.summary)
		assert.False(t, failed, text)
		assert.Equal(t, step.want, text)
	}

	text, failed := call("anything")
	assert.True(t, failed)
	assert.Equal(t, `the model answered "maybe", not an issue number`, text)

	footer := "\nSession https://rocketclaw.example/s/Y2hhdA\n\ngrep said nothing"
	assert.Equal(t, []post{
		{"#harness", "", "New harness issue: grep hides errors" + footer},
		{"C9", "100.1", "Reported again: grep is silent on errors" + footer},
		{"#harness", "", "New harness issue: read rejects absolute paths" + footer},
	}, posts)
	require.Len(t, prompts, 3)
	assert.Contains(t, prompts[0], "Known issues:\n1. grep hides errors\n\nNew report:")
	assert.Contains(t, prompts[2], "1. grep hides errors\n2. read rejects absolute paths\n")

	tx, err := db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	issues, err := recentIssues(t.Context(), tx)
	require.NoError(t, err)
	assert.Equal(t, []issue{{1, "grep hides errors", "C9", "100.1"}, {2, "read rejects absolute paths", "C9", "100.3"}}, issues)
}

func TestRunNamesMissingSettings(t *testing.T) {
	t.Parallel()

	err := Run(t.Context(), func(key string) string {
		return map[string]string{"HARNESS_FEEDBACK_SLACK_TOKEN": "xoxb", "HARNESS_FEEDBACK_MODEL": "m"}[key]
	})
	require.EqualError(t, err, "harness feedback needs HARNESS_FEEDBACK_DB, HARNESS_FEEDBACK_SLACK_CHANNEL, HARNESS_FEEDBACK_MODEL_URL, HARNESS_FEEDBACK_MODEL_KEY, HARNESS_FEEDBACK_WEB_ORIGIN, ROCKETCLAW_CONVERSATION_ID")
}
