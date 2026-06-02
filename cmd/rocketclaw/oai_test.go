package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type oaiLoginRoundTrip func(*http.Request) (*http.Response, error)

func (f oaiLoginRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRunOAIShowsHelp(t *testing.T) {
	output := captureStdout(t, func() error {
		return runOAI(nil)
	})

	assert.Contains(t, output, "rocketclaw oai login [--headless]")
}

func TestRunOAIHelpAliases(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		output := captureStdout(t, func() error {
			return runOAI(args)
		})

		assert.Contains(t, output, "Authenticate with ChatGPT")
	}
}

func TestRunOAILoginShowsHelp(t *testing.T) {
	output := captureStdout(t, func() error {
		return runOAILogin([]string{"--help"})
	})

	assert.Contains(t, output, "rocketclaw oai login [--headless]")
}

func TestRunOAILoginHeadlessShowsHelp(t *testing.T) {
	output := captureStdout(t, func() error {
		return runOAILogin([]string{"--headless", "--help"})
	})

	assert.Contains(t, output, "Authenticate with ChatGPT")
}

func TestRunOAIDispatchesLoginHelp(t *testing.T) {
	output := captureStdout(t, func() error {
		return runOAI([]string{"login", "--help"})
	})

	assert.Contains(t, output, "Authenticate with ChatGPT")
}

func TestRunOAIRejectsUnknownCommand(t *testing.T) {
	err := runOAI([]string{"nope"})

	require.EqualError(t, err, `unknown oai command "nope"`)
}

func TestRunOAILoginRejectsUnknownArgument(t *testing.T) {
	err := runOAILogin([]string{"--bad"})

	require.EqualError(t, err, `unknown oai login argument "--bad"`)
}

func TestRunOAILoginHeadlessCompletesDeviceFlow(t *testing.T) {
	workspace := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(cwd)) })

	workspace, err = os.Getwd()
	require.NoError(t, err)

	base := http.DefaultClient.Transport

	t.Cleanup(func() { http.DefaultClient.Transport = base })

	response := func(status int, body string) *http.Response {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
	}

	http.DefaultClient.Transport = oaiLoginRoundTrip(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://auth.openai.com/api/accounts/deviceauth/usercode":
			require.Equal(t, http.MethodPost, req.Method)
			return response(http.StatusOK, `{"device_auth_id":"dev-123","user_code":"CODE-456","interval":"1"}`), nil
		case "https://auth.openai.com/api/accounts/deviceauth/token":
			require.Equal(t, http.MethodPost, req.Method)
			return response(http.StatusOK, `{"authorization_code":"auth-code","code_verifier":"verifier"}`), nil
		case "https://auth.openai.com/oauth/token":
			require.Equal(t, http.MethodPost, req.Method)
			require.NoError(t, req.ParseForm())
			require.Equal(t, "authorization_code", req.Form.Get("grant_type"))
			require.Equal(t, "auth-code", req.Form.Get("code"))

			return response(http.StatusOK, `{"access_token":"access","refresh_token":"refresh","expires_in":60}`), nil
		default:
			t.Fatalf("unexpected OAuth request URL %s", req.URL)
			return nil, nil
		}
	})

	synctest.Test(t, func(t *testing.T) {
		output := captureStdout(t, func() error {
			return runOAILogin([]string{"--headless"})
		})

		authPath := filepath.Join(workspace, ".rocketclaw", "auth.json")

		assert.Contains(t, output, "Open https://auth.openai.com/codex/device and enter code: CODE-456")
		assert.Contains(t, output, "Saved OpenAI ChatGPT OAuth token to "+authPath)
	})

	data, err := os.ReadFile(filepath.Join(workspace, ".rocketclaw", "auth.json"))
	require.NoError(t, err)
	assert.Contains(t, string(data), `"refresh": "refresh"`)
}

func TestRunOAILoginHeadlessWrapsDeviceAuthorizationFailure(t *testing.T) {
	base := http.DefaultClient.Transport

	t.Cleanup(func() { http.DefaultClient.Transport = base })

	http.DefaultClient.Transport = oaiLoginRoundTrip(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "https://auth.openai.com/api/accounts/deviceauth/usercode", req.URL.String())

		return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader("denied\n")), Header: make(http.Header)}, nil
	})

	err := runOAILogin([]string{"--headless"})
	require.ErrorContains(t, err, "login with ChatGPT OAuth: device authorization failed (400): denied")
}
