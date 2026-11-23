package slackconnector

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionMentionRecipient(t *testing.T) {
	transport := &slackNameRoundTripper{info: map[string]string{
		"U1": `{"ok":true,"user":{"id":"U1","is_email_confirmed":true,"profile":{"email":"person@example.test"}}}`,
		"U2": `{"ok":true,"user":{"id":"U2","is_email_confirmed":false,"profile":{"email":"person@example.test"}}}`,
		"U3": `{"ok":true,"user":{"id":"U3","is_email_confirmed":true,"is_bot":true,"profile":{"email":"person@example.test"}}}`,
		"U4": `{"ok":true,"user":{"id":"U4","is_email_confirmed":true,"deleted":true,"profile":{"email":"person@example.test"}}}`,
	}}
	connector := newSlackNamesConnector(transport)

	for _, test := range []struct{ id, email string }{
		{id: "U1", email: "person@example.test"},
		{id: "U2"},
		{id: "U3"},
		{id: "U4"},
		{id: "S1"},
		{id: "U1,U2"},
	} {
		t.Run(test.id, func(t *testing.T) {
			email, err := connector.SessionMentionRecipient(t.Context(), test.id)
			require.NoError(t, err)
			require.Equal(t, test.email, email)
		})
	}

	_, _, infos := transport.counts()
	require.Equal(t, 4, infos)
}

func TestSessionMentionDirectMessage(t *testing.T) {
	const link = "https://example.test/s/Y2hhdA"

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NoError(t, r.ParseForm())

		switch r.URL.Path {
		case "/conversations.open":
			assert.Equal(t, "U123", r.Form.Get("users"))

			_, _ = io.WriteString(w, `{"ok":true,"channel":{"id":"D456"}}`)
		case "/chat.postMessage":
			assert.Equal(t, "D456", r.Form.Get("channel"))
			assert.Equal(t, "You were mentioned in a session. <"+link+"|Open session>", r.Form.Get("text"))
			assert.Equal(t, "false", r.Form.Get("unfurl_links"))
			assert.Equal(t, "false", r.Form.Get("unfurl_media"))

			_, _ = io.WriteString(w, `{"ok":true,"channel":"D456","ts":"10.20"}`)
		default:
			t.Errorf("unexpected directory API path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(api.Close)

	connector := newTestConnectorWithOptions(api.URL, nil, &primaryTextRouterMock{
		WebURLFunc: func(id string) (string, error) {
			assert.Equal(t, "chat", id)
			return link, nil
		},
	})

	require.NoError(t, connector.SendSessionMention(t.Context(), "U123", "chat"))
}
