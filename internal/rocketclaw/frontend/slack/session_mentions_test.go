package slackconnector

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionMentionSendsOnlyTheLinkBeforeLookingUpAWebLogin(t *testing.T) {
	t.Parallel()

	link := "https://claw.example.test/s/Y2hhdA"

	var calls []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		assert.NoError(t, r.ParseForm())
		w.Header().Set("Content-Type", "application/json")

		var payload string

		switch r.URL.Path {
		case "/conversations.open":
			assert.Equal(t, "U42", r.Form.Get("users"))

			payload = `{"ok":true,"channel":{"id":"D42"}}`
		case "/chat.postMessage":
			assert.Equal(t, "D42", r.Form.Get("channel"))
			assert.Equal(t, link, r.Form.Get("text"))
			assert.Equal(t, "false", r.Form.Get("unfurl_links"))
			assert.Equal(t, "false", r.Form.Get("unfurl_media"))
			assert.Empty(t, r.Form.Get("blocks"))

			payload = `{"ok":true,"channel":"D42","ts":"1.1"}`
		case "/users.info":
			payload = `{"ok":false,"error":"user_not_found"}`
		default:
			t.Errorf("unexpected method %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}

		if _, err := io.WriteString(w, payload); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	require.NoError(t, connector.SendSessionMention(t.Context(), "U42", link))
	require.Empty(t, connector.SlackMentionEmail(t.Context(), "U42"))
	require.Equal(t, []string{"/conversations.open", "/chat.postMessage", "/users.info"}, calls)
}
