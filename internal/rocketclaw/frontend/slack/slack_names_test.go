package slackconnector

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

const (
	slackNamesUsersJSON  = `{"ok":true,"members":[{"id":"U1","real_name":"Alan Smith","profile":{"display_name":"alan"}},{"id":"U2","real_name":"Bea","profile":{"display_name":""}}]}`
	slackNamesGroupsJSON = `{"ok":true,"usergroups":[{"id":"S1","handle":"cs-operators"}]}`
)

type slackNameRoundTripper struct {
	mu              sync.Mutex
	usersList       int
	userGroups      int
	usersInfo       int
	users, groups   string
	info            map[string]string
	includeDisabled []string
}

func (rt *slackNameRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, fmt.Errorf("slack test transport: %w", err)
	}

	body, _ := io.ReadAll(req.Body)
	form, _ := url.ParseQuery(string(body))

	rt.mu.Lock()
	defer rt.mu.Unlock()

	respond := func(payload string) *http.Response {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}
	}

	switch strings.TrimPrefix(req.URL.Path, "/") {
	case "users.list":
		rt.usersList++
		return respond(rt.users), nil
	case "usergroups.list":
		rt.userGroups++
		rt.includeDisabled = append(rt.includeDisabled, form.Get("include_disabled"))

		return respond(rt.groups), nil
	case "users.info":
		rt.usersInfo++
		if payload, ok := rt.info[form.Get("user")]; ok {
			return respond(payload), nil
		}

		return respond(`{"ok":false,"error":"user_not_found"}`), nil
	default:
		return respond(`{"ok":false,"error":"unknown_method"}`), nil
	}
}

func (rt *slackNameRoundTripper) counts() (usersList, userGroups, usersInfo int) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	return rt.usersList, rt.userGroups, rt.usersInfo
}

func newSlackNamesConnector(transport http.RoundTripper) *Connector {
	connector := newTestConnector("http://slack.test")
	connector.teamID = "T1"
	connector.api = slack.New("xoxb-test", slack.OptionAPIURL("http://slack.test/"), slack.OptionHTTPClient(&http.Client{Transport: transport}))

	return connector
}

func TestSlackNamesResolvesDisplayRealAndGroup(t *testing.T) {
	transport := &slackNameRoundTripper{users: slackNamesUsersJSON, groups: slackNamesGroupsJSON}
	connector := newSlackNamesConnector(transport)
	names := connector.SlackNames(t.Context(), []string{"U1", "U2", "S1"})
	require.Equal(t, map[string]string{"U1": "alan", "U2": "Bea", "S1": "cs-operators"}, names)

	usersList, userGroups, usersInfo := transport.counts()
	require.Equal(t, 1, usersList)
	require.Equal(t, 1, userGroups)
	require.Equal(t, 0, usersInfo)
	require.Equal(t, []string{"true"}, transport.includeDisabled)
}

func TestSlackNamesReloadsAfter8Hours(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &slackNameRoundTripper{users: slackNamesUsersJSON, groups: slackNamesGroupsJSON}
		connector := newSlackNamesConnector(transport)
		require.Equal(t, "alan", connector.SlackNames(t.Context(), []string{"U1"})["U1"])
		require.Equal(t, "cs-operators", connector.SlackNames(t.Context(), []string{"S1"})["S1"])

		usersList, userGroups, _ := transport.counts()
		require.Equal(t, 1, usersList)
		require.Equal(t, 1, userGroups)

		transport.users = `{"ok":true,"members":[{"id":"U1","profile":{"display_name":"renamed"}}]}`
		transport.groups = `{"ok":true,"usergroups":[{"id":"S1","handle":"ops"}]}`

		time.Sleep(8*time.Hour - time.Nanosecond)
		require.Equal(t, "alan", connector.SlackNames(t.Context(), []string{"U1"})["U1"])

		usersList, userGroups, _ = transport.counts()
		require.Equal(t, 1, usersList)
		require.Equal(t, 1, userGroups)
		time.Sleep(time.Nanosecond)
		require.Equal(t, "renamed", connector.SlackNames(t.Context(), []string{"U1"})["U1"])
		require.Equal(t, "ops", connector.SlackNames(t.Context(), []string{"S1"})["S1"])

		usersList, userGroups, _ = transport.counts()
		require.Equal(t, 2, usersList)
		require.Equal(t, 2, userGroups)
	})
}

func TestSlackNamesUserInfoMissIsCached(t *testing.T) {
	transport := &slackNameRoundTripper{
		users:  slackNamesUsersJSON,
		groups: slackNamesGroupsJSON,
		info: map[string]string{
			"W9": `{"ok":true,"user":{"id":"W9","real_name":"Wendy","profile":{"display_name":"wendy"}}}`,
		},
	}
	connector := newSlackNamesConnector(transport)
	require.Equal(t, map[string]string{"W9": "wendy"}, connector.SlackNames(t.Context(), []string{"W9", "Umissing"}))

	_, _, usersInfo := transport.counts()
	require.Equal(t, 2, usersInfo)
	require.Equal(t, map[string]string{"W9": "wendy"}, connector.SlackNames(t.Context(), []string{"W9", "Umissing"}))

	_, _, usersInfo = transport.counts()
	require.Equal(t, 2, usersInfo)
}

func TestSlackNamesConcurrentColdLoadSharesOneList(t *testing.T) {
	transport := &slackNameRoundTripper{users: slackNamesUsersJSON, groups: slackNamesGroupsJSON}
	connector := newSlackNamesConnector(transport)

	var g errgroup.Group
	g.Go(func() error {
		connector.SlackNames(context.Background(), []string{"U1"})
		return nil
	})
	g.Go(func() error {
		connector.SlackNames(context.Background(), []string{"S1"})
		return nil
	})
	require.NoError(t, g.Wait())

	usersList, userGroups, usersInfo := transport.counts()
	require.Equal(t, 1, usersList)
	require.Equal(t, 1, userGroups)
	require.Equal(t, 0, usersInfo)
}

func TestSlackNamesFailedReloadKeepsNamesAndRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &slackNameRoundTripper{users: slackNamesUsersJSON, groups: slackNamesGroupsJSON}
		connector := newSlackNamesConnector(transport)
		require.Equal(t, "alan", connector.SlackNames(t.Context(), []string{"U1"})["U1"])

		transport.users = `{"ok":false,"error":"fatal_error"}`

		time.Sleep(8 * time.Hour)
		require.Equal(t, "alan", connector.SlackNames(t.Context(), []string{"U1"})["U1"])

		usersList, userGroups, _ := transport.counts()
		require.Equal(t, 2, usersList)
		require.Equal(t, 1, userGroups)
		require.Equal(t, "alan", connector.SlackNames(t.Context(), []string{"U1"})["U1"])
		require.Empty(t, connector.SlackTagsMatching(t.Context(), "zzz"))

		usersList, userGroups, _ = transport.counts()
		require.Equal(t, 2, usersList, "a failed load is not retried within a minute")
		require.Equal(t, 1, userGroups)

		time.Sleep(time.Minute)
		require.Equal(t, "alan", connector.SlackNames(t.Context(), []string{"U1"})["U1"])

		usersList, userGroups, _ = transport.counts()
		require.Equal(t, 3, usersList)
		require.Equal(t, 1, userGroups)
	})
}

func TestSlackNamesUserInfoCanceledIsNotCached(t *testing.T) {
	transport := &slackNameRoundTripper{
		users:  slackNamesUsersJSON,
		groups: slackNamesGroupsJSON,
		info: map[string]string{
			"W9": `{"ok":true,"user":{"id":"W9","real_name":"Wendy","profile":{"display_name":"wendy"}}}`,
		},
	}
	connector := newSlackNamesConnector(transport)
	require.Equal(t, "alan", connector.SlackNames(t.Context(), []string{"U1"})["U1"])

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	require.Empty(t, connector.SlackNames(canceled, []string{"W9"}))
	require.Equal(t, map[string]string{"W9": "wendy"}, connector.SlackNames(t.Context(), []string{"W9"}))
}

func TestSlackNamesUserGroupsMissingScopeStaysFresh(t *testing.T) {
	transport := &slackNameRoundTripper{users: slackNamesUsersJSON, groups: `{"ok":false,"error":"missing_scope"}`}
	connector := newSlackNamesConnector(transport)
	require.Equal(t, map[string]string{"U1": "alan"}, connector.SlackNames(t.Context(), []string{"U1", "S1"}))

	usersList, userGroups, _ := transport.counts()
	require.Equal(t, 1, usersList)
	require.Equal(t, 1, userGroups)
	require.Equal(t, map[string]string{"U1": "alan"}, connector.SlackNames(t.Context(), []string{"U1", "S1"}))

	usersList, userGroups, _ = transport.counts()
	require.Equal(t, 1, usersList)
	require.Equal(t, 1, userGroups)
}

func TestSlackNamesChannelUsesFactsWithoutSlack(t *testing.T) {
	transport := &slackNameRoundTripper{users: slackNamesUsersJSON, groups: slackNamesGroupsJSON}
	connector := newSlackNamesConnector(transport)
	facts := newTestChannelFacts()
	facts.ChannelFactFunc = func(_ context.Context, workspace, channel string) (string, bool, error) {
		require.Equal(t, "T1", workspace)
		require.Equal(t, "C1", channel)

		return "general", true, nil
	}
	connector.facts = facts
	require.Equal(t, map[string]string{"C1": "general"}, connector.SlackNames(t.Context(), []string{"C1"}))

	usersList, userGroups, usersInfo := transport.counts()
	require.Equal(t, 0, usersList)
	require.Equal(t, 0, userGroups)
	require.Equal(t, 0, usersInfo)
}

func TestSlackTagsMatching(t *testing.T) {
	transport := &slackNameRoundTripper{users: slackNamesUsersJSON, groups: slackNamesGroupsJSON}
	connector := newSlackNamesConnector(transport)

	ids := connector.SlackTagsMatching(t.Context(), "cs-operators")
	slices.Sort(ids)
	require.Equal(t, []string{"S1"}, ids)

	ids = connector.SlackTagsMatching(t.Context(), "ALAN")
	slices.Sort(ids)
	require.Equal(t, []string{"U1"}, ids)
	require.Empty(t, connector.SlackTagsMatching(t.Context(), "missing"))

	usersList, userGroups, usersInfo := transport.counts()
	require.Equal(t, 1, usersList)
	require.Equal(t, 1, userGroups)
	require.Equal(t, 0, usersInfo)
	require.Equal(t, "alan", connector.SlackNames(t.Context(), []string{"U1"})["U1"])

	usersList, userGroups, usersInfo = transport.counts()
	require.Equal(t, 1, usersList)
	require.Equal(t, 1, userGroups)
	require.Equal(t, 0, usersInfo)
}

func TestSlackTagsMatchingFailedLoad(t *testing.T) {
	transport := &slackNameRoundTripper{users: `{"ok":false,"error":"fatal_error"}`, groups: slackNamesGroupsJSON}
	connector := newSlackNamesConnector(transport)
	require.Empty(t, connector.SlackTagsMatching(t.Context(), "alan"))

	usersList, userGroups, usersInfo := transport.counts()
	require.Equal(t, 1, usersList)
	require.Equal(t, 0, userGroups)
	require.Equal(t, 0, usersInfo)
}
