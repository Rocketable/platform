//nolint:exhaustruct // Tests use sparse HTTP/OpenAI DTO literals for focused assertions.
package oai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type loginOutput chan string

func (w loginOutput) Write(p []byte) (int, error) {
	w <- string(p)

	return len(p), nil
}

type loginBrowserResult struct {
	path string
	err  error
}

func TestTokenRoundTripUsesWorkspaceAuthFile(t *testing.T) {
	workspace := t.TempDir()
	testAuthPath(t, workspace)

	token := Token{Refresh: "refresh", Access: "access", Expires: time.Now().UnixMilli(), AccountID: "acc-123"}
	require.NoError(t, SaveToken(workspace, token))

	got, err := LoadToken(workspace)
	require.NoError(t, err)
	require.Equal(t, token, got)
}

func TestAuthFilePathDefaultsToCurrentDirectory(t *testing.T) {
	got, err := AuthFilePath("")
	require.NoError(t, err)
	require.True(t, filepath.IsAbs(got))
	require.True(t, strings.HasSuffix(got, filepath.Join(".rocketclaw", "auth.json")))
}

func TestLoadTokenRejectsInvalidTokenFiles(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "invalid json", data: `{not-json`, want: "parse OpenAI OAuth token"},
		{name: "missing refresh", data: `{"access":"access"}`, want: "missing refresh token"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			testAuthPath(t, workspace)
			path, err := AuthFilePath(workspace)
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, []byte(tt.data), 0o600))

			_, err = LoadToken(workspace)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestLoadTokenReportsMissingAuthFile(t *testing.T) {
	_, err := LoadToken(t.TempDir())
	require.ErrorContains(t, err, "read OpenAI OAuth token")
}

func TestExtractAccountIDPrefersIDToken(t *testing.T) {
	got := extractAccountID(tokenResponse{
		IDToken:     testJWT(map[string]any{"chatgpt_account_id": "from-id"}),
		AccessToken: testJWT(map[string]any{"chatgpt_account_id": "from-access"}),
	})

	require.Equal(t, "from-id", got)
}

func TestExtractAccountIDFallsBackToAccessNestedClaim(t *testing.T) {
	got := extractAccountID(tokenResponse{
		IDToken: testJWT(map[string]any{"email": "test@example.com"}),
		AccessToken: testJWT(map[string]any{
			"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "nested"},
		}),
	})

	require.Equal(t, "nested", got)
}

func TestTokenFromResponseFallsBackToAccessOrganizationAndDefaultExpiry(t *testing.T) {
	before := time.Now().Add(59 * time.Minute).UnixMilli()
	token := tokenFromResponse(tokenResponse{AccessToken: testJWT(map[string]any{"organizations": []map[string]string{{"id": "org-123"}}}), RefreshToken: "refresh"})
	after := time.Now().Add(61 * time.Minute).UnixMilli()

	require.Equal(t, "refresh", token.Refresh)
	require.NotEmpty(t, token.Access)
	require.Equal(t, "org-123", token.AccountID)
	require.GreaterOrEqual(t, token.Expires, before)
	require.LessOrEqual(t, token.Expires, after)
}

func TestAuthorizeURLIncludesOAuthParameters(t *testing.T) {
	got, err := url.Parse(authorizeURL("http://127.0.0.1/callback", pkceCodes{challenge: "challenge"}, "state-123"))
	require.NoError(t, err)

	query := got.Query()
	require.Equal(t, issuer, got.Scheme+"://"+got.Host)
	require.Equal(t, "/oauth/authorize", got.Path)
	require.Equal(t, clientID, query.Get("client_id"))
	require.Equal(t, "http://127.0.0.1/callback", query.Get("redirect_uri"))
	require.Equal(t, "challenge", query.Get("code_challenge"))
	require.Equal(t, "S256", query.Get("code_challenge_method"))
	require.Equal(t, "state-123", query.Get("state"))
	require.Equal(t, originator, query.Get("originator"))
}

func TestExchangeCodePostsAuthorizationCodeForm(t *testing.T) {
	base := http.DefaultClient.Transport
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, http.MethodPost, req.Method)
		require.Equal(t, issuer+"/oauth/token", req.URL.String())
		require.Equal(t, "application/x-www-form-urlencoded", req.Header.Get("Content-Type"))
		require.NoError(t, req.ParseForm())
		require.Equal(t, "authorization_code", req.Form.Get("grant_type"))
		require.Equal(t, "code-123", req.Form.Get("code"))
		require.Equal(t, "http://localhost/callback", req.Form.Get("redirect_uri"))
		require.Equal(t, clientID, req.Form.Get("client_id"))
		require.Equal(t, "verifier-123", req.Form.Get("code_verifier"))

		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"access_token":"access","refresh_token":"refresh","expires_in":60}`)), Header: make(http.Header)}, nil
	})

	t.Cleanup(func() { http.DefaultClient.Transport = base })

	response, err := exchangeCode(context.Background(), "code-123", "http://localhost/callback", "verifier-123")
	require.NoError(t, err)
	require.Equal(t, "access", response.AccessToken)
	require.Equal(t, "refresh", response.RefreshToken)
	require.Equal(t, int64(60), response.ExpiresIn)
}

func TestLoginBrowserCompletesCallbackAndSavesToken(t *testing.T) {
	requireLoginBrowserPortAvailable(t)

	workspace := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	access := testJWT(map[string]any{"chatgpt_account_id": "acc-browser"})
	formCh := make(chan url.Values, 1)
	base := http.DefaultClient.Transport
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost {
			return nil, fmt.Errorf("method = %s", req.Method)
		}

		if req.URL.String() != issuer+"/oauth/token" {
			return nil, fmt.Errorf("url = %s", req.URL.String())
		}

		if contentType := req.Header.Get("Content-Type"); contentType != "application/x-www-form-urlencoded" {
			return nil, fmt.Errorf("content type = %s", contentType)
		}

		if err := req.ParseForm(); err != nil {
			return nil, fmt.Errorf("parse token request form: %w", err)
		}

		formCh <- req.Form

		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"access_token":"` + access + `","refresh_token":"refresh-browser","expires_in":60}`)), Header: make(http.Header)}, nil
	})

	t.Cleanup(func() { http.DefaultClient.Transport = base })

	state, done := startLoginBrowser(ctx, t, workspace)
	body := sendLoginBrowserCallback(ctx, t, done, url.Values{"state": {state}, "code": {"code-123"}})
	require.Equal(t, "Authorization successful. You can close this window.", body)

	select {
	case got := <-done:
		require.NoError(t, got.err)

		wantPath, err := AuthFilePath(workspace)
		require.NoError(t, err)
		require.Equal(t, wantPath, got.path)
	case <-ctx.Done():
		t.Fatalf("wait for LoginBrowser completion: %v", ctx.Err())
	}

	select {
	case form := <-formCh:
		require.Equal(t, "authorization_code", form.Get("grant_type"))
		require.Equal(t, "code-123", form.Get("code"))
		require.Equal(t, fmt.Sprintf("http://localhost:%d/auth/callback", defaultLoginPort), form.Get("redirect_uri"))
		require.Equal(t, clientID, form.Get("client_id"))
		require.NotEmpty(t, form.Get("code_verifier"))
	case <-ctx.Done():
		t.Fatalf("wait for token request: %v", ctx.Err())
	}

	token, err := LoadToken(workspace)
	require.NoError(t, err)
	require.Equal(t, "refresh-browser", token.Refresh)
	require.Equal(t, access, token.Access)
	require.Equal(t, "acc-browser", token.AccountID)
}

func TestLoginBrowserReportsCallbackErrors(t *testing.T) {
	tests := []struct {
		name  string
		query func(string) url.Values
		body  string
		want  string
	}{
		{
			name: "authorization error",
			query: func(string) url.Values {
				return url.Values{"error": {"access_denied"}, "error_description": {"Denied"}}
			},
			body: "Authorization failed. You can close this window.",
			want: "OAuth callback: Denied",
		},
		{
			name: "invalid state",
			query: func(string) url.Values {
				return url.Values{"state": {"wrong"}, "code": {"code-123"}}
			},
			body: "Invalid authorization state. You can close this window.",
			want: "OAuth callback: invalid OAuth state",
		},
		{
			name: "missing code",
			query: func(state string) url.Values {
				return url.Values{"state": {state}}
			},
			body: "Missing authorization code. You can close this window.",
			want: "OAuth callback: missing OAuth authorization code",
		},
	}

	base := http.DefaultClient.Transport

	t.Cleanup(func() { http.DefaultClient.Transport = base })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireLoginBrowserPortAvailable(t)

			http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return nil, fmt.Errorf("unexpected token request to %s", req.URL)
			})

			workspace := t.TempDir()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			state, done := startLoginBrowser(ctx, t, workspace)
			body := sendLoginBrowserCallback(ctx, t, done, tt.query(state))
			require.Equal(t, tt.body, body)

			select {
			case got := <-done:
				require.Empty(t, got.path)
				require.ErrorContains(t, got.err, tt.want)
			case <-ctx.Done():
				t.Fatalf("wait for LoginBrowser callback error: %v", ctx.Err())
			}
		})
	}
}

func TestPostTokenReportsHTTPAndDecodeErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		err    error
		want   string
	}{
		{name: "transport error", err: errors.New("offline"), want: "send token request"},
		{name: "http error", status: http.StatusBadRequest, body: "denied\n", want: "token request failed (400): denied"},
		{name: "invalid json", status: http.StatusOK, body: `{not-json`, want: "decode token response"},
	}

	base := http.DefaultClient.Transport

	t.Cleanup(func() { http.DefaultClient.Transport = base })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				require.Equal(t, http.MethodPost, req.Method)
				require.Equal(t, issuer+"/oauth/token", req.URL.String())

				if tt.err != nil {
					return nil, tt.err
				}

				return &http.Response{StatusCode: tt.status, Body: io.NopCloser(strings.NewReader(tt.body)), Header: make(http.Header)}, nil
			})

			_, err := postToken(context.Background(), url.Values{"grant_type": {"refresh_token"}})
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestPollDevicePendingAndCompletes(t *testing.T) {
	workspace := t.TempDir()
	access := testJWT(map[string]any{"chatgpt_account_id": "acc-123"})
	phase := "pending"

	base := http.DefaultClient.Transport
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case issuer + "/api/accounts/deviceauth/token":
			require.Equal(t, http.MethodPost, req.Method)
			require.Equal(t, "application/json", req.Header.Get("Content-Type"))

			var body map[string]string
			require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
			require.Equal(t, "dev-123", body["device_auth_id"])
			require.Equal(t, "user-456", body["user_code"])

			if phase == "pending" {
				return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
			}

			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"authorization_code":"auth-code","code_verifier":"verifier"}`)), Header: make(http.Header)}, nil
		case issuer + "/oauth/token":
			require.NoError(t, req.ParseForm())
			require.Equal(t, "authorization_code", req.Form.Get("grant_type"))
			require.Equal(t, "auth-code", req.Form.Get("code"))
			require.Equal(t, issuer+"/deviceauth/callback", req.Form.Get("redirect_uri"))
			require.Equal(t, clientID, req.Form.Get("client_id"))
			require.Equal(t, "verifier", req.Form.Get("code_verifier"))

			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"access_token":"` + access + `","refresh_token":"refresh-next","expires_in":60}`)), Header: make(http.Header)}, nil
		}

		t.Fatalf("unexpected request URL %s", req.URL)

		return nil, nil
	})

	t.Cleanup(func() { http.DefaultClient.Transport = base })

	path, done, err := pollDeviceIn(context.Background(), workspace, config.DefaultWorkDir, "dev-123", "user-456")
	require.NoError(t, err)
	require.False(t, done)
	require.Empty(t, path)

	phase = "complete"
	path, done, err = pollDeviceIn(context.Background(), workspace, config.DefaultWorkDir, "dev-123", "user-456")
	require.NoError(t, err)
	require.True(t, done)

	wantPath, err := AuthFilePath(workspace)
	require.NoError(t, err)
	require.Equal(t, wantPath, path)

	token, err := LoadToken(workspace)
	require.NoError(t, err)
	require.Equal(t, "refresh-next", token.Refresh)
	require.Equal(t, access, token.Access)
	require.Equal(t, "acc-123", token.AccountID)
}

func TestPollDeviceReportsUnexpectedStatus(t *testing.T) {
	base := http.DefaultClient.Transport
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, issuer+"/api/accounts/deviceauth/token", req.URL.String())

		return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(strings.NewReader("server unavailable\n")), Header: make(http.Header)}, nil
	})

	t.Cleanup(func() { http.DefaultClient.Transport = base })

	path, done, err := pollDeviceIn(context.Background(), t.TempDir(), config.DefaultWorkDir, "dev-123", "user-456")
	require.Empty(t, path)
	require.False(t, done)
	require.ErrorContains(t, err, "device token request failed (500): server unavailable")
}

func TestPollDeviceReportsTransportAndDecodeErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		body string
		want string
	}{
		{name: "transport error", err: errors.New("offline"), want: "send device token request"},
		{name: "invalid json", body: `{not-json`, want: "decode device token response"},
	}

	base := http.DefaultClient.Transport

	t.Cleanup(func() { http.DefaultClient.Transport = base })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				require.Equal(t, issuer+"/api/accounts/deviceauth/token", req.URL.String())

				if tt.err != nil {
					return nil, tt.err
				}

				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tt.body)), Header: make(http.Header)}, nil
			})

			path, done, err := pollDeviceIn(context.Background(), t.TempDir(), config.DefaultWorkDir, "dev-123", "user-456")
			require.Empty(t, path)
			require.False(t, done)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestLoginDevicePrintsCodeAndStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	base := http.DefaultClient.Transport
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, http.MethodPost, req.Method)
		require.Equal(t, issuer+"/api/accounts/deviceauth/usercode", req.URL.String())
		require.Equal(t, "application/json", req.Header.Get("Content-Type"))
		require.Equal(t, "rocketclaw", req.Header.Get("User-Agent"))

		var body map[string]string
		require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
		require.Equal(t, clientID, body["client_id"])

		cancel()

		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"device_auth_id":"dev-123","user_code":"CODE-456","interval":"1"}`)), Header: make(http.Header)}, nil
	})

	t.Cleanup(func() { http.DefaultClient.Transport = base })

	var out strings.Builder

	path, err := LoginDevice(ctx, t.TempDir(), &out)
	require.Empty(t, path)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "wait for device authorization")
	require.Equal(t, "Open "+issuer+"/codex/device and enter code: CODE-456\n", out.String())
}

func TestLoginDeviceFallsBackToDefaultInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	base := http.DefaultClient.Transport
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, issuer+"/api/accounts/deviceauth/usercode", req.URL.String())
		cancel()

		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"device_auth_id":"dev-123","user_code":"CODE-456","interval":"not-a-duration"}`)), Header: make(http.Header)}, nil
	})

	t.Cleanup(func() { http.DefaultClient.Transport = base })

	path, err := LoginDevice(ctx, t.TempDir(), io.Discard)
	require.Empty(t, path)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "wait for device authorization")
}

func TestLoginDeviceReportsAuthorizationResponseErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{name: "http error", status: http.StatusBadRequest, body: "denied\n", want: "device authorization failed (400): denied"},
		{name: "invalid json", status: http.StatusOK, body: `{not-json`, want: "decode device authorization response"},
	}

	base := http.DefaultClient.Transport

	t.Cleanup(func() { http.DefaultClient.Transport = base })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				require.Equal(t, issuer+"/api/accounts/deviceauth/usercode", req.URL.String())

				return &http.Response{StatusCode: tt.status, Body: io.NopCloser(strings.NewReader(tt.body)), Header: make(http.Header)}, nil
			})

			_, err := LoginDevice(context.Background(), t.TempDir(), io.Discard)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestSaveTokenResponsePropagatesWriteError(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.WriteFile(workspace, []byte("file"), 0o600))

	path, err := saveTokenResponse(workspace, tokenResponse{AccessToken: "access", RefreshToken: "refresh"})
	require.Empty(t, path)
	require.ErrorContains(t, err, "create OpenAI OAuth token dir")
}

func TestSaveTokenReportsWriteError(t *testing.T) {
	workspace := t.TempDir()
	path, err := AuthFilePath(workspace)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(path, 0o755))

	err = SaveToken(workspace, Token{Access: "access", Refresh: "refresh"})
	require.ErrorContains(t, err, "write OpenAI OAuth token")
}

func TestGeneratePKCEProducesVerifierAndChallenge(t *testing.T) {
	pkce, err := generatePKCE()
	require.NoError(t, err)
	require.NotEmpty(t, pkce.verifier)
	require.NotEmpty(t, pkce.challenge)
	require.NotEqual(t, pkce.verifier, pkce.challenge)
	require.NotContains(t, pkce.verifier, "=")
	require.NotContains(t, pkce.challenge, "=")
}

func TestParseClaimsRejectsMalformedTokens(t *testing.T) {
	for _, token := range []string{"", "one.two", "one.!.three", "one.two.three", "one." + base64.RawURLEncoding.EncodeToString([]byte(`{not-json}`)) + ".three"} {
		t.Run(token, func(t *testing.T) {
			_, ok := parseClaims(token)
			require.False(t, ok)
		})
	}
}

func TestCodexStreamingResponseRequiresCompletionEvent(t *testing.T) {
	resp := &http.Response{Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"msg\"}}\n")), Header: make(http.Header)}

	got, err := codexStreamingResponse(resp)
	if got != nil {
		defer func() { _ = got.Body.Close() }()
	}

	require.ErrorContains(t, err, "missing completion event")
}

func TestCodexStreamingResponseReportsMalformedEvents(t *testing.T) {
	resp := &http.Response{Body: io.NopCloser(strings.NewReader("data: {not-json}\n")), Header: make(http.Header)}

	got, err := codexStreamingResponse(resp)
	if got != nil {
		defer func() { _ = got.Body.Close() }()
	}

	require.Nil(t, got)
	require.ErrorContains(t, err, "parse Codex stream response")
}

func TestCodexStreamingResponseUsesCompletedResponseWithoutOutputItems(t *testing.T) {
	resp := &http.Response{Body: io.NopCloser(strings.NewReader("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp\",\"output\":[]}}\n")), Header: make(http.Header)}

	got, err := codexStreamingResponse(resp)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, got.Body.Close()) })

	data, err := io.ReadAll(got.Body)
	require.NoError(t, err)
	require.JSONEq(t, `{"id":"resp","output":[]}`, string(data))
}

func TestNewChatGPTClientRequiresSavedToken(t *testing.T) {
	workspace := t.TempDir()
	testAuthPath(t, workspace)

	client, err := NewChatGPTClient(workspace)
	require.Nil(t, client)
	require.Error(t, err)

	require.NoError(t, SaveToken(workspace, Token{Refresh: "refresh", Access: "access", Expires: time.Now().Add(time.Hour).UnixMilli()}))
	client, err = NewChatGPTClient(workspace)
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestStripInputIDsRemovesIDsWhenStoreFalse(t *testing.T) {
	req := requestWithBody(`{"store":false,"context_management":[{"type":"compaction"}],"max_output_tokens":100,"input":[{"id":"item-1","type":"message"},{"id":"item-2","type":"function_call"}]}`)

	_, err := cleanCodexRequest(req, true)
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
	require.NotContains(t, body, "context_management")
	require.NotContains(t, body, "max_output_tokens")
	items := body["input"].([]any)
	require.NotContains(t, items[0].(map[string]any), "id")
	require.NotContains(t, items[1].(map[string]any), "id")
}

func TestCleanCodexRequestLeavesNonJSONAndInvalidJSONBodies(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "non-json", contentType: "text/plain", body: `plain body`},
		{name: "invalid-json", contentType: "application/json", body: `{not-json`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := requestWithBody(tt.body)
			req.Header.Set("Content-Type", tt.contentType)

			_, err := cleanCodexRequest(req, true)
			require.NoError(t, err)
			data, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.Equal(t, tt.body, string(data))
		})
	}
}

func TestCleanCodexRequestReportsBodyReadError(t *testing.T) {
	errRead := errors.New("read failed")
	req := requestWithBody("")
	req.Body = io.NopCloser(iotest.ErrReader(errRead))

	metadata, err := cleanCodexRequest(req, true)
	require.False(t, metadata.hasCompact)
	require.ErrorIs(t, err, errRead)
	require.ErrorContains(t, err, "read OpenAI request body")
}

func TestCleanCodexRequestSkipsNonObjectMetadataAndInputItems(t *testing.T) {
	req := requestWithBody(`{"store":false,"context_management":["skip",{"compact_threshold":"many"}],"input":["skip",{"id":"item-1","type":"message"}]}`)

	metadata, err := cleanCodexRequest(req, false)
	require.NoError(t, err)
	require.False(t, metadata.hasCompact)

	var body map[string]any
	require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
	require.NotContains(t, body, "context_management")
	items := body["input"].([]any)
	require.Equal(t, "skip", items[0])
	require.NotContains(t, items[1].(map[string]any), "id")
}

func TestCleanCodexRequestNoopsForNonPostOrNilBody(t *testing.T) {
	req := requestWithBody(`{"input":[]}`)
	req.Method = http.MethodGet

	metadata, err := cleanCodexRequest(req, true)
	require.NoError(t, err)
	require.False(t, metadata.hasCompact)

	data, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.JSONEq(t, `{"input":[]}`, string(data))

	req = requestWithBody(`{"input":[]}`)
	req.Body = nil

	metadata, err = cleanCodexRequest(req, true)
	require.NoError(t, err)
	require.False(t, metadata.hasCompact)
}

func TestCleanCodexRequestExtractsCompactionThreshold(t *testing.T) {
	req := requestWithBody(`{"context_management":[{"type":"compaction","compact_threshold":12.5}],"input":[{"type":"message"}]}`)

	metadata, err := cleanCodexRequest(req, false)
	require.NoError(t, err)
	require.True(t, metadata.hasCompact)
	require.InDelta(t, 12.5, metadata.compactThreshold, 0)

	data, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.NotContains(t, string(data), "context_management")
}

func TestStripInputIDsKeepsIDsWhenStoreTrue(t *testing.T) {
	req := requestWithBody(`{"store":true,"input":[{"id":"item-1","type":"message"}]}`)

	_, err := cleanCodexRequest(req, true)
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
	items := body["input"].([]any)
	require.Equal(t, "item-1", items[0].(map[string]any)["id"])
}

func TestTransportAddsOAuthHeadersAndStripsBodyIDs(t *testing.T) {
	workspace := t.TempDir()
	testAuthPath(t, workspace)
	require.NoError(t, SaveToken(workspace, Token{Refresh: "refresh", Access: "access", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountID: "acc-123"}))

	transport := &transport{workspace: workspace, base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "Bearer access", req.Header.Get("Authorization"))
		require.Equal(t, "acc-123", req.Header.Get("Chatgpt-Account-Id"))

		data, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.NotContains(t, string(data), `"id"`)
		require.Contains(t, string(data), `"stream":true`)
		require.Contains(t, string(data), `"instructions":""`)

		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"msg\",\"type\":\"message\"}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp\",\"output\":[]}}\n")), Header: make(http.Header)}, nil
	})}
	req := requestWithPathAndBody("/backend-api/codex/responses", `{"store":false,"input":[{"id":"item-1","type":"message"}]}`)
	req.Header.Set("Authorization", "Bearer dummy")

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })
	require.Equal(t, http.StatusOK, resp.StatusCode)
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.JSONEq(t, `{"id":"resp","output":[{"id":"msg","type":"message"}]}`, string(data))
}

func TestTransportUsesDefaultTransportWhenBaseNil(t *testing.T) {
	workspace := t.TempDir()
	testAuthPath(t, workspace)
	require.NoError(t, SaveToken(workspace, Token{Refresh: "refresh", Access: "access", Expires: time.Now().Add(time.Hour).UnixMilli()}))

	base := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "/backend-api/codex/models", req.URL.Path)
		require.Equal(t, "Bearer access", req.Header.Get("Authorization"))

		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Header: make(http.Header)}, nil
	})

	t.Cleanup(func() { http.DefaultTransport = base })

	transport := &transport{workspace: workspace}
	resp, err := transport.RoundTrip(requestWithPathAndBody("/backend-api/codex/models", `{}`))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })

	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.JSONEq(t, `{"data":[]}`, string(data))
}

func TestTransportReportsTokenAndBaseErrors(t *testing.T) {
	t.Run("token error", func(t *testing.T) {
		transport := &transport{workspace: t.TempDir(), base: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("base transport should not be called without a token")

			return nil, nil
		})}

		resp, err := transport.RoundTrip(requestWithPathAndBody("/backend-api/codex/models", `{}`))
		if resp != nil {
			t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })
		}

		require.Nil(t, resp)
		require.ErrorContains(t, err, "read OpenAI OAuth token")
	})

	t.Run("base error", func(t *testing.T) {
		workspace := t.TempDir()
		testAuthPath(t, workspace)
		require.NoError(t, SaveToken(workspace, Token{Refresh: "refresh", Access: "access", Expires: time.Now().Add(time.Hour).UnixMilli()}))

		errSend := errors.New("offline")
		transport := &transport{workspace: workspace, base: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errSend
		})}

		resp, err := transport.RoundTrip(requestWithPathAndBody("/backend-api/codex/models", `{}`))
		if resp != nil {
			t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })
		}

		require.Nil(t, resp)
		require.ErrorIs(t, err, errSend)
		require.ErrorContains(t, err, "send OpenAI request")
	})
}

func TestTransportPrefixesCompactionWhenUsageExceedsThreshold(t *testing.T) {
	body, compactCalls := runAutoCompactTransportTest(t,
		`{"model":"gpt-5.5","instructions":"be brief","store":false,"context_management":[{"type":"compaction","compact_threshold":10}],"input":[{"id":"item-1","type":"message"}]}`,
		codexStream(`{"id":"resp","usage":{"total_tokens":100},"output":[]}`, `{"id":"msg","type":"message"}`),
		`{"output":[{"id":"cmp_1","type":"compaction","encrypted_content":"sealed"}]}`,
	)

	require.Equal(t, 1, compactCalls)
	require.JSONEq(t, `{"id":"resp","usage":{"total_tokens":100},"output":[{"encrypted_content":"sealed","id":"cmp_1","type":"compaction"},{"id":"msg","type":"message"}]}`, body)
}

func TestTransportSkipsCompactionWithUnansweredFunctionCall(t *testing.T) {
	body, compactCalls := runAutoCompactTransportTest(t,
		`{"model":"gpt-5.5","context_management":[{"compact_threshold":10}],"input":[{"type":"function_call","call_id":"call_1"}]}`,
		codexStream(`{"id":"resp","usage":{"total_tokens":100},"output":[]}`, `{"id":"msg","type":"message"}`),
		`{"output":[{"id":"cmp_1","type":"compaction","encrypted_content":"sealed"}]}`,
	)

	require.Equal(t, 0, compactCalls)
	require.JSONEq(t, `{"id":"resp","usage":{"total_tokens":100},"output":[{"id":"msg","type":"message"}]}`, body)
}

func TestCodexInputHasUnansweredFunctionCallRejectsMalformedInput(t *testing.T) {
	require.False(t, codexInputHasUnansweredFunctionCall(json.RawMessage(`{not-json`)))
}

func TestTransportCompactsWithAnsweredFunctionCall(t *testing.T) {
	_, compactCalls := runAutoCompactTransportTest(t,
		`{"model":"gpt-5.5","context_management":[{"compact_threshold":10}],"input":[{"type":"function_call","call_id":"call_1"},{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`,
		codexStream(`{"id":"resp","usage":{"total_tokens":100},"output":[]}`, `{"id":"msg","type":"message"}`),
		`{"output":[{"id":"cmp_1","type":"compaction","encrypted_content":"sealed"}]}`,
	)

	require.Equal(t, 1, compactCalls)
}

func TestTransportSkipsCompactionWhenResponseAlreadyHasCompaction(t *testing.T) {
	_, compactCalls := runAutoCompactTransportTest(t,
		`{"model":"gpt-5.5","context_management":[{"compact_threshold":10}],"input":[{"type":"message"}]}`,
		codexStream(`{"id":"resp","usage":{"total_tokens":100},"output":[]}`, `{"id":"cmp_existing","type":"compaction","encrypted_content":"sealed"}`),
		`{"output":[{"id":"cmp_1","type":"compaction","encrypted_content":"sealed"}]}`,
	)

	require.Equal(t, 0, compactCalls)
}

func TestTransportSkipsCompactionBelowThreshold(t *testing.T) {
	_, compactCalls := runAutoCompactTransportTest(t,
		`{"model":"gpt-5.5","context_management":[{"compact_threshold":100}],"input":[{"type":"message"}]}`,
		codexStream(`{"id":"resp","usage":{"total_tokens":10},"output":[]}`, `{"id":"msg","type":"message"}`),
		`{"output":[{"id":"cmp_1","type":"compaction","encrypted_content":"sealed"}]}`,
	)

	require.Equal(t, 0, compactCalls)
}

func TestTransportNormalizesCompactionSummary(t *testing.T) {
	body, compactCalls := runAutoCompactTransportTest(t,
		`{"model":"gpt-5.5","context_management":[{"compact_threshold":10}],"input":[{"type":"message"}]}`,
		codexStream(`{"id":"resp","usage":{"total_tokens":100},"output":[]}`, `{"id":"msg","type":"message"}`),
		`{"output":[{"id":"cmp_1","type":"compaction_summary","encrypted_content":"sealed"}]}`,
	)

	require.Equal(t, 1, compactCalls)
	require.Contains(t, body, `"type":"compaction"`)
	require.NotContains(t, body, `compaction_summary`)
}

func TestTransportLeavesResponseWhenCompactResponseHasNoCompaction(t *testing.T) {
	body, compactCalls := runAutoCompactTransportTest(t,
		`{"model":"gpt-5.5","context_management":[{"compact_threshold":10}],"input":[{"type":"message"}]}`,
		codexStream(`{"id":"resp","usage":{"total_tokens":100},"output":[]}`, `{"id":"msg","type":"message"}`),
		`{"output":[{"id":"compact-msg","type":"message"}]}`,
	)

	require.Equal(t, 1, compactCalls)
	require.JSONEq(t, `{"id":"resp","usage":{"total_tokens":100},"output":[{"id":"msg","type":"message"}]}`, body)
	require.NotContains(t, body, `compact-msg`)
}

func TestCodexCompactionHandlesErrorAndFallbackBranches(t *testing.T) {
	errRead := errors.New("read failed")
	errGetBody := errors.New("get body failed")
	errCompact := errors.New("compact failed")
	errCompactRead := errors.New("compact read failed")
	responseBody := `{"usage":{"total_tokens":100},"output":[]}`
	originalBody := `{"usage":{"total_tokens":100},"output":[{"id":"msg","type":"message"}]}`
	requestBody := `{"model":"gpt-5.5","input":[{"type":"message"}]}`
	metadata := codexRequestMetadata{hasCompact: true, compactThreshold: 10}
	compactBase := func(status int, compactBody io.Reader, errRoundTrip error) func(*testing.T, *int) http.RoundTripper {
		return func(t *testing.T, compactCalls *int) http.RoundTripper {
			t.Helper()

			return roundTripFunc(func(req *http.Request) (*http.Response, error) {
				*compactCalls++

				require.Equal(t, "/backend-api/codex/responses/compact", req.URL.Path)

				data, errRead := io.ReadAll(req.Body)
				require.NoError(t, errRead)
				require.JSONEq(t, requestBody, string(data))

				if errRoundTrip != nil {
					return nil, errRoundTrip
				}

				return &http.Response{StatusCode: status, Body: io.NopCloser(compactBody), Header: make(http.Header)}, nil
			})
		}
	}

	tests := []struct {
		name            string
		requestBody     string
		responseBody    io.Reader
		getBodyErr      error
		base            func(*testing.T, *int) http.RoundTripper
		wantErr         error
		wantErrContains string
		wantCompact     int
		wantBody        string
	}{
		{
			name:            "response read error",
			responseBody:    iotest.ErrReader(errRead),
			wantErr:         errRead,
			wantErrContains: "read Codex response",
		},
		{
			name:            "malformed response",
			responseBody:    strings.NewReader(`{not-json`),
			wantErrContains: "parse Codex response",
		},
		{
			name:            "get body error",
			responseBody:    strings.NewReader(responseBody),
			getBodyErr:      errGetBody,
			wantErr:         errGetBody,
			wantErrContains: "read Codex compact source request",
		},
		{
			name:            "malformed source request",
			requestBody:     `{not-json`,
			responseBody:    strings.NewReader(responseBody),
			wantErrContains: "parse Codex compact source request",
		},
		{
			name:            "malformed output",
			responseBody:    strings.NewReader(`{"usage":{"total_tokens":100},"output":{}}`),
			wantErrContains: "parse Codex output",
		},
		{
			name:            "compact request error",
			responseBody:    strings.NewReader(responseBody),
			base:            compactBase(http.StatusOK, strings.NewReader(`{}`), errCompact),
			wantErr:         errCompact,
			wantErrContains: "send Codex compact request",
			wantCompact:     1,
		},
		{
			name:            "compact read error",
			responseBody:    strings.NewReader(responseBody),
			base:            compactBase(http.StatusOK, iotest.ErrReader(errCompactRead), nil),
			wantErr:         errCompactRead,
			wantErrContains: "read Codex compact response",
			wantCompact:     1,
		},
		{
			name:         "compact http error",
			responseBody: strings.NewReader(originalBody),
			base:         compactBase(http.StatusBadGateway, strings.NewReader(`denied`), nil),
			wantCompact:  1,
			wantBody:     originalBody,
		},
		{
			name:            "malformed compact response",
			responseBody:    strings.NewReader(responseBody),
			base:            compactBase(http.StatusOK, strings.NewReader(`{not-json`), nil),
			wantErrContains: "parse Codex compact response",
			wantCompact:     1,
		},
		{
			name:         "malformed compact item type",
			responseBody: strings.NewReader(originalBody),
			base:         compactBase(http.StatusOK, strings.NewReader(`{"output":[{"type":{}}]}`), nil),
			wantCompact:  1,
			wantBody:     originalBody,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := tt.requestBody
			if body == "" {
				body = requestBody
			}

			req := requestWithPathAndBody("/backend-api/codex/responses", body)
			if tt.getBodyErr != nil {
				req.GetBody = func() (io.ReadCloser, error) { return nil, tt.getBodyErr }
			}

			compactCalls := 0

			var base http.RoundTripper = roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("compact transport should not be called")

				return nil, nil
			})
			if tt.base != nil {
				base = tt.base(t, &compactCalls)
			}

			transport := &transport{base: base}
			resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(tt.responseBody), Header: make(http.Header)}

			got, err := transport.codexCompaction(context.Background(), req, resp, &metadata)
			if got != nil {
				t.Cleanup(func() { require.NoError(t, got.Body.Close()) })
			}

			require.Equal(t, tt.wantCompact, compactCalls)

			if tt.wantErrContains != "" {
				require.ErrorContains(t, err, tt.wantErrContains)

				if tt.wantErr != nil {
					require.ErrorIs(t, err, tt.wantErr)
				}

				return
			}

			require.NoError(t, err)

			data, errRead := io.ReadAll(got.Body)
			require.NoError(t, errRead)
			require.JSONEq(t, tt.wantBody, string(data))
		})
	}
}

func TestTransportRefreshesExpiredOAuthToken(t *testing.T) {
	workspace := t.TempDir()
	testAuthPath(t, workspace)
	require.NoError(t, SaveToken(workspace, Token{Refresh: "refresh", Access: "old", Expires: time.Now().Add(-time.Minute).UnixMilli(), AccountID: "acc-old"}))

	access := testJWT(map[string]any{"organizations": []map[string]string{{"id": "acc-new"}}})
	base := http.DefaultClient.Transport
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, issuer+"/oauth/token", req.URL.String())

		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"access_token":"` + access + `","refresh_token":"next-refresh","expires_in":3600}`)), Header: make(http.Header)}, nil
	})

	t.Cleanup(func() { http.DefaultClient.Transport = base })

	transport := &transport{workspace: workspace, base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "Bearer "+access, req.Header.Get("Authorization"))
		require.Equal(t, "acc-new", req.Header.Get("Chatgpt-Account-Id"))

		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Header: make(http.Header)}, nil
	})}

	resp, err := transport.RoundTrip(requestWithPathAndBody("/backend-api/codex/models", `{}`))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })

	token, err := LoadToken(workspace)
	require.NoError(t, err)
	require.Equal(t, "next-refresh", token.Refresh)
	require.Equal(t, "acc-new", token.AccountID)
}

func TestTransportRefreshPreservesStoredAccountID(t *testing.T) {
	workspace := t.TempDir()
	testAuthPath(t, workspace)
	require.NoError(t, SaveToken(workspace, Token{Refresh: "refresh", Access: "old", Expires: time.Now().Add(-time.Minute).UnixMilli(), AccountID: "acc-old"}))

	base := http.DefaultClient.Transport
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, issuer+"/oauth/token", req.URL.String())

		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"access_token":"next-access","refresh_token":"next-refresh","expires_in":3600}`)), Header: make(http.Header)}, nil
	})

	t.Cleanup(func() { http.DefaultClient.Transport = base })

	transport := &transport{workspace: workspace, base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "Bearer next-access", req.Header.Get("Authorization"))
		require.Equal(t, "acc-old", req.Header.Get("Chatgpt-Account-Id"))

		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Header: make(http.Header)}, nil
	})}

	resp, err := transport.RoundTrip(requestWithPathAndBody("/backend-api/codex/models", `{}`))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })

	token, err := LoadToken(workspace)
	require.NoError(t, err)
	require.Equal(t, "next-refresh", token.Refresh)
	require.Equal(t, "acc-old", token.AccountID)
}

func TestTransportReportsRefreshError(t *testing.T) {
	workspace := t.TempDir()
	testAuthPath(t, workspace)
	require.NoError(t, SaveToken(workspace, Token{Refresh: "refresh", Access: "old", Expires: time.Now().Add(-time.Minute).UnixMilli()}))

	errRefresh := errors.New("offline")
	base := http.DefaultClient.Transport
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, issuer+"/oauth/token", req.URL.String())

		return nil, errRefresh
	})

	t.Cleanup(func() { http.DefaultClient.Transport = base })

	transport := &transport{workspace: workspace, base: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("base transport should not be called when refresh fails")

		return nil, nil
	})}

	resp, err := transport.RoundTrip(requestWithPathAndBody("/backend-api/codex/models", `{}`))
	if resp != nil {
		t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })
	}

	require.Nil(t, resp)
	require.ErrorIs(t, err, errRefresh)
	require.ErrorContains(t, err, "send token request")
}

func TestTransportTreatsTrailingSlashResponsesPathAsStreaming(t *testing.T) {
	workspace := t.TempDir()
	testAuthPath(t, workspace)
	require.NoError(t, SaveToken(workspace, Token{Refresh: "refresh", Access: "access", Expires: time.Now().Add(time.Hour).UnixMilli()}))

	transport := &transport{workspace: workspace, base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		data, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.Contains(t, string(data), `"stream":true`)

		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp\",\"output\":[]}}\n")), Header: make(http.Header)}, nil
	})}
	req := requestWithPathAndBody("/backend-api/codex/responses/", `{"store":false,"input":[]}`)

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })

	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.JSONEq(t, `{"id":"resp","output":[]}`, string(data))
}

func TestTransportLeavesCompactRequestsAsJSON(t *testing.T) {
	workspace := t.TempDir()
	testAuthPath(t, workspace)
	require.NoError(t, SaveToken(workspace, Token{Refresh: "refresh", Access: "access", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountID: "acc-123"}))

	transport := &transport{workspace: workspace, base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "/backend-api/codex/responses/compact", req.URL.Path)
		require.Equal(t, "Bearer access", req.Header.Get("Authorization"))
		require.Equal(t, "acc-123", req.Header.Get("Chatgpt-Account-Id"))

		data, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.NotContains(t, string(data), `"stream"`)
		require.NotContains(t, string(data), `"instructions"`)

		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"id":"resp_1","object":"response.compaction","output":[{"id":"cmp_1","type":"compaction","encrypted_content":"sealed"}]}`)), Header: make(http.Header)}, nil
	})}

	req := requestWithPathAndBody("/backend-api/codex/responses/compact", `{"model":"gpt-5.5","input":[{"id":"item-1","type":"message"}]}`)
	req.Header.Set("Authorization", "Bearer dummy")

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.JSONEq(t, `{"id":"resp_1","object":"response.compaction","output":[{"id":"cmp_1","type":"compaction","encrypted_content":"sealed"}]}`, string(data))
}

func TestTransportLeavesNonResponseRequestsAsJSON(t *testing.T) {
	workspace := t.TempDir()
	testAuthPath(t, workspace)
	require.NoError(t, SaveToken(workspace, Token{Refresh: "refresh", Access: "access", Expires: time.Now().Add(time.Hour).UnixMilli()}))

	transport := &transport{workspace: workspace, base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "/backend-api/codex/models", req.URL.Path)
		require.Equal(t, "Bearer access", req.Header.Get("Authorization"))

		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Header: make(http.Header)}, nil
	})}
	req := requestWithPathAndBody("/backend-api/codex/models", `{}`)

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })

	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.JSONEq(t, `{"data":[]}`, string(data))
}

func TestCodexStreamingResponseReportsStreamErrors(t *testing.T) {
	errRead := errors.New("stream read failed")
	tests := []struct {
		name    string
		body    io.ReadCloser
		want    string
		wantErr error
	}{
		{
			name:    "read error",
			body:    io.NopCloser(iotest.ErrReader(errRead)),
			want:    "read Codex stream response",
			wantErr: errRead,
		},
		{
			name: "malformed completed response",
			body: io.NopCloser(strings.NewReader(`data: {"type":"response.completed","response":[]}`)),
			want: "parse Codex completed response",
		},
		{
			name: "missing completion",
			body: io.NopCloser(strings.NewReader(`data: {"type":"response.in_progress"}`)),
			want: "missing completion event",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := &http.Response{Body: tt.body, Header: make(http.Header)}

			t.Cleanup(func() { require.NoError(t, response.Body.Close()) })

			resp, err := codexStreamingResponse(response)
			if resp != nil {
				t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })
			}

			require.Nil(t, resp)
			require.ErrorContains(t, err, tt.want)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			}
		})
	}
}

func runAutoCompactTransportTest(t *testing.T, requestBody, streamBody, compactBody string) (body string, compactCalls int) {
	t.Helper()

	workspace := t.TempDir()
	testAuthPath(t, workspace)
	require.NoError(t, SaveToken(workspace, Token{Refresh: "refresh", Access: "access", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountID: "acc-123"}))

	transport := &transport{workspace: workspace, base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "Bearer access", req.Header.Get("Authorization"))
		require.Equal(t, "acc-123", req.Header.Get("Chatgpt-Account-Id"))

		if strings.HasSuffix(req.URL.Path, "/responses/compact") {
			compactCalls++

			data, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.Contains(t, string(data), `"model":"gpt-5.5"`)
			require.Contains(t, string(data), `"input"`)
			require.NotContains(t, string(data), `context_management`)
			require.NotContains(t, string(data), `"id"`)

			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(compactBody)), Header: make(http.Header)}, nil
		}

		data, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.NotContains(t, string(data), `context_management`)

		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(streamBody)), Header: make(http.Header)}, nil
	})}

	resp, err := transport.RoundTrip(requestWithPathAndBody("/backend-api/codex/responses", requestBody))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })

	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return string(data), compactCalls
}

func codexStream(completed string, items ...string) string {
	var b strings.Builder
	for _, item := range items {
		b.WriteString("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":")
		b.WriteString(item)
		b.WriteString("}\n")
	}

	b.WriteString("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":")
	b.WriteString(completed)
	b.WriteString("}\n")

	return b.String()
}

func requestWithBody(body string) *http.Request {
	return requestWithPathAndBody("/responses", body)
}

func requestWithPathAndBody(path, body string) *http.Request {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.com"+path, strings.NewReader(body))
	if err != nil {
		panic(err)
	}

	req.Header.Set("Content-Type", "application/json")

	return req
}

func startLoginBrowser(ctx context.Context, t *testing.T, workspace string) (state string, done <-chan loginBrowserResult) {
	t.Helper()

	output := make(loginOutput, 8)
	doneCh := make(chan loginBrowserResult, 1)

	go func() {
		path, err := LoginBrowser(ctx, workspace, output)
		doneCh <- loginBrowserResult{path: path, err: err}
	}()

	var text strings.Builder

	for {
		select {
		case chunk := <-output:
			text.WriteString(chunk)
			lines := strings.Split(strings.TrimSpace(text.String()), "\n")
			authURL := lines[len(lines)-1]

			u, err := url.Parse(authURL)
			if err != nil {
				continue
			}

			if state := u.Query().Get("state"); state != "" {
				return state, doneCh
			}
		case got := <-doneCh:
			t.Fatalf("LoginBrowser() returned before printing auth URL: path=%q err=%v", got.path, got.err)
		case <-ctx.Done():
			t.Fatalf("wait for LoginBrowser auth URL: %v", ctx.Err())
		}
	}
}

func sendLoginBrowserCallback(ctx context.Context, t *testing.T, done <-chan loginBrowserResult, query url.Values) string {
	t.Helper()

	client := http.Client{Transport: http.DefaultTransport}
	callbackURL := url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", defaultLoginPort), Path: "/auth/callback", RawQuery: query.Encode()}

	for {
		select {
		case got := <-done:
			t.Fatalf("LoginBrowser() returned before OAuth callback completed: path=%q err=%v", got.path, got.err)
		default:
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, callbackURL.String(), http.NoBody)
		require.NoError(t, err)

		resp, err := client.Do(req)
		if err == nil {
			data, errRead := io.ReadAll(resp.Body)
			errClose := resp.Body.Close()

			require.NoError(t, errRead)
			require.NoError(t, errClose)
			require.Equal(t, http.StatusOK, resp.StatusCode)

			return string(data)
		}

		select {
		case got := <-done:
			t.Fatalf("LoginBrowser() returned before OAuth callback completed: path=%q err=%v", got.path, got.err)
		case <-ctx.Done():
			t.Fatalf("send OAuth callback: %v; last error: %v", ctx.Err(), err)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func requireLoginBrowserPortAvailable(t *testing.T) {
	t.Helper()

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", defaultLoginPort))
	require.NoError(t, err)
	require.NoError(t, listener.Close())
}

func testJWT(payload map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))

	data, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}

	body := base64.RawURLEncoding.EncodeToString(data)

	return header + "." + body + ".sig"
}

func testAuthPath(t *testing.T, workspace string) {
	t.Helper()

	path, err := AuthFilePath(workspace)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(workspace, ".rocketclaw", "auth.json"), path)
}
