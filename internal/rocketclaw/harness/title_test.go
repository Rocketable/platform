package harness

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateTitle(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("é", 120)

	for _, tc := range []struct {
		name, titleModel, request, recent, reply, want, model, effort string
	}{
		{name: "default model", request: "debug 500 errors in production", reply: "\n  Production 500 errors\nignored", want: "Production 500 errors", model: "gpt-6-luna", effort: "low"},
		{name: "configured model", titleModel: "work/titles#minimal", request: "hey", reply: "Greeting", want: "Greeting", model: "work/titles", effort: "minimal"},
		{name: "conversation", request: "ORIGINAL " + strings.Repeat("a", 3000) + " CLIPPED", recent: "OLD " + strings.Repeat("b", 9000) + " RECENT", reply: long, want: strings.Repeat("é", 97) + "...", model: "gpt-6-luna", effort: "low"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var model, effort, input string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model        string `json:"model"`
					Instructions string `json:"instructions"`
					Input        string `json:"input"`
					Reasoning    struct {
						Effort string `json:"effort"`
					} `json:"reasoning"`
				}
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				assert.Equal(t, titlePrompt, body.Instructions)

				model, effort, input = body.Model, body.Reasoning.Effort, body.Input

				w.Header().Set("Content-Type", "application/json")

				reply, _ := json.Marshal(tc.reply)
				_, _ = w.Write([]byte(`{"id":"resp","object":"response","created_at":0,"status":"completed","model":"title-model","output":[` +
					`{"id":"note","type":"message","status":"completed","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"We need the title only.","annotations":[]}]},` +
					`{"id":"final","type":"message","status":"completed","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":` + string(reply) + `,"annotations":[]}]}]}`))
			}))
			t.Cleanup(server.Close)

			client := openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(server.URL), option.WithUnsafeAllowHTTP())

			title, err := GenerateTitle(t.Context(), testResolverForClient(&client), tc.titleModel, tc.request, tc.recent)
			require.NoError(t, err)
			assert.Equal(t, tc.want, title)
			assert.Equal(t, []string{tc.model, tc.effort}, []string{model, effort})

			if tc.recent == "" {
				assert.Equal(t, "Here is the first message of the conversation:\n\n<message>\n"+tc.request+"\n</message>\n\nReply with only the title.", input)
				return
			}

			assert.LessOrEqual(t, len(input), 8000)
			assert.Greater(t, len(input), 7900)

			for _, kept := range []string{"Here is the conversation so far:", "Original request:\nORIGINAL", "Recent conversation:\n", "RECENT\n</conversation>"} {
				assert.Contains(t, input, kept)
			}

			assert.NotContains(t, input, "CLIPPED")
			assert.NotContains(t, input, "OLD")
		})
	}
}
