package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/oai"
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

	assert.Contains(t, output, "rocketclaw oai login [provider] [--headless]")
	assert.Contains(t, output, "rocketclaw oai list")
	assert.Contains(t, output, "rocketclaw oai logout [provider]")
	assert.Contains(t, output, "\n  login   Authenticate")
	assert.NotContains(t, output, "\n\tlogin")
}

func TestRunOAIHelpAliases(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}, {"login", "--help"}, {"login", "--headless", "--help"}, {"login", "--help", "work"}, {"login", "work", "--help"}, {"list", "--help"}, {"logout", "--help"}} {
		output := captureStdout(t, func() error {
			return runOAI(args)
		})

		assert.Contains(t, output, "Authenticate with ChatGPT")
	}
}

func TestRunOAIRejectsUnknownCommand(t *testing.T) {
	err := runOAI([]string{"nope"})

	require.EqualError(t, err, `unknown oai command "nope"`)
}

func TestRunOAILoginAcceptsDefaultAndNamedProvider(t *testing.T) {
	for _, tt := range []struct {
		name     string
		args     []string
		provider string
	}{
		{name: "omitted default", args: []string{"login", "--headless"}, provider: "openai"},
		{name: "explicit default", args: []string{"login", "openai", "--headless"}, provider: "openai"},
		{name: "named before flag", args: []string{"login", "work", "--headless"}, provider: "work"},
		{name: "named after flag", args: []string{"login", "--headless", "work"}, provider: "work"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeOAITestConfig(t, t.TempDir(), workspace, "api_key", "api_key", nil)
			stubDeviceLogin(t, "refresh-"+tt.provider)

			synctest.Test(t, func(t *testing.T) {
				output := captureStdout(t, func() error { return runOAI(tt.args) })
				assert.Contains(t, output, tt.provider)
				assert.NotContains(t, output, "refresh-"+tt.provider)
				assert.NotContains(t, output, "access-secret")
			})

			token, _, err := oai.LoadTokenIn(workspace, config.DefaultRuntimeDir, tt.provider)
			require.NoError(t, err)
			assert.Equal(t, "refresh-"+tt.provider, token.Refresh)
		})
	}
}

func TestRunOAILoginUsesConfiguredWorkspace(t *testing.T) {
	configDir := t.TempDir()
	workspace := t.TempDir()
	writeOAITestConfig(t, configDir, workspace, "api_key", "api_key", nil)
	stubDeviceLogin(t, "workspace-refresh")

	synctest.Test(t, func(t *testing.T) {
		captureStdout(t, func() error { return runOAI([]string{"login", "work", "--headless"}) })
	})

	has, err := oai.HasTokenIn(workspace, config.DefaultRuntimeDir, "work")
	require.NoError(t, err)
	assert.True(t, has)
	_, err = os.Stat(filepath.Join(configDir, config.DefaultRuntimeDir, "auth.json"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRunOAILoginRewritesSelectedConfigProvider(t *testing.T) {
	configDir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(configDir, "workspace"), 0o700))
	configPath := writeOAITestConfig(t, configDir, "workspace", "api_key", "api_key", nil)
	requestedMode := os.FileMode(0o640) | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	require.NoError(t, os.Chmod(configPath, requestedMode))
	info, err := os.Stat(configPath)
	require.NoError(t, err)
	wantMode := info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
	require.NotZero(t, wantMode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky))
	stubDeviceLogin(t, "selected-refresh")

	synctest.Test(t, func(t *testing.T) {
		captureStdout(t, func() error { return runOAI([]string{"login", "work", "--headless"}) })
	})

	var raw struct {
		Workspace string `json:"workspace"`
		OpenAI    struct {
			Auth string `json:"rocketcode_auth"`
		} `json:"openai"`
		Providers map[string]struct {
			Auth string `json:"rocketcode_auth"`
		} `json:"providers"`
	}
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Equal(t, "workspace", raw.Workspace)
	assert.Equal(t, "api_key", raw.OpenAI.Auth)
	assert.Equal(t, "chatgpt", raw.Providers["work"].Auth)
	info, err = os.Stat(configPath)
	require.NoError(t, err)
	assert.Equal(t, wantMode, info.Mode()&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky))
}

func TestRunOAILoginPreservesUnknownConfigFields(t *testing.T) {
	configDir := t.TempDir()
	extra := `,"future_top":{"sentinel":"top-secret"}`
	writeOAITestConfig(t, configDir, t.TempDir(), "api_key", "api_key", map[string]string{"future_nested": "nested-secret"}, extra)
	stubDeviceLogin(t, "unknown-refresh")

	synctest.Test(t, func(t *testing.T) {
		captureStdout(t, func() error { return runOAI([]string{"login", "work", "--headless"}) })
	})

	data, err := os.ReadFile(filepath.Join(configDir, defaultConfigPath))
	require.NoError(t, err)
	assert.Contains(t, string(data), `"future_top"`)
	assert.Contains(t, string(data), `"top-secret"`)
	assert.Contains(t, string(data), `"future_nested"`)
	assert.Contains(t, string(data), `"nested-secret"`)
}

func TestRunOAILoginRestoresConfigWhenCredentialWriteFails(t *testing.T) {
	configDir := t.TempDir()
	workspace := t.TempDir()
	configPath := writeOAITestConfig(t, configDir, workspace, "api_key", "api_key", nil)
	original, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, config.DefaultRuntimeDir, "auth.json"), 0o700))
	stubDeviceLogin(t, "rollback-refresh")

	synctest.Test(t, func(t *testing.T) {
		err = runOAI([]string{"login", "work", "--headless"})
	})
	require.Error(t, err)

	got, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Equal(t, original, got)
}

func TestConcurrentDifferentProviderLoginsPreserveBothConfigChanges(t *testing.T) {
	configDir := t.TempDir()
	workspace := t.TempDir()
	configPath := writeOAITestConfig(t, configDir, workspace, "api_key", "api_key", nil)

	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, providerID := range []string{"openai", "work"} {
		go func() {
			<-start
			_, err := commitOAILogin(providerID, oai.Token{Refresh: providerID + "-refresh"})
			errs <- err
		}()
	}
	close(start)
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	var raw struct {
		OpenAI struct {
			Auth string `json:"rocketcode_auth"`
		} `json:"openai"`
		Providers map[string]struct {
			Auth string `json:"rocketcode_auth"`
		} `json:"providers"`
	}
	require.NoError(t, json.Unmarshal(data, &raw))
	require.Equal(t, "chatgpt", raw.OpenAI.Auth)
	require.Equal(t, "chatgpt", raw.Providers["work"].Auth)
}

func TestRunOAIListReportsProviderAuthWithoutSecrets(t *testing.T) {
	workspace := t.TempDir()
	writeOAITestConfig(t, t.TempDir(), workspace, "api_key", "chatgpt", map[string]string{"api_key": "named-api-secret"})

	output := captureStdout(t, func() error { return runOAI([]string{"list"}) })

	assert.Equal(t, "openai (default) api_key present\nwork chatgpt missing\n", output)
	assert.NotContains(t, output, "default-api-secret")
	assert.NotContains(t, output, "named-api-secret")
	assert.NotContains(t, output, workspace)
}

func TestRunOAILogoutRemovesOnlySelectedProviderToken(t *testing.T) {
	workspace := t.TempDir()
	writeOAITestConfig(t, t.TempDir(), workspace, "chatgpt", "chatgpt", nil)
	_, err := oai.ReplaceTokenIn(workspace, config.DefaultRuntimeDir, "openai", oai.Token{Refresh: "default-refresh"})
	require.NoError(t, err)
	_, err = oai.ReplaceTokenIn(workspace, config.DefaultRuntimeDir, "work", oai.Token{Refresh: "work-refresh"})
	require.NoError(t, err)

	output := captureStdout(t, func() error { return runOAI([]string{"logout", "work"}) })
	assert.Contains(t, output, "work")
	assert.NotContains(t, output, "refresh")

	hasDefault, err := oai.HasTokenIn(workspace, config.DefaultRuntimeDir, "openai")
	require.NoError(t, err)
	hasWork, err := oai.HasTokenIn(workspace, config.DefaultRuntimeDir, "work")
	require.NoError(t, err)
	assert.True(t, hasDefault)
	assert.False(t, hasWork)
}

func TestRunOAILogoutDoesNotRewriteAuthMode(t *testing.T) {
	workspace := t.TempDir()
	configPath := writeOAITestConfig(t, t.TempDir(), workspace, "api_key", "chatgpt", nil)
	_, err := oai.ReplaceTokenIn(workspace, config.DefaultRuntimeDir, "work", oai.Token{Refresh: "work-refresh"})
	require.NoError(t, err)
	original, err := os.ReadFile(configPath)
	require.NoError(t, err)

	captureStdout(t, func() error { return runOAI([]string{"logout", "work"}) })

	got, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Equal(t, original, got)
}

func TestRunOAIRejectsUnknownProvider(t *testing.T) {
	workspace := t.TempDir()
	writeOAITestConfig(t, t.TempDir(), workspace, "api_key", "api_key", nil)

	err := runOAI([]string{"logout", "unknown"})
	require.EqualError(t, err, `unknown provider "unknown"`)
	_, err = os.Stat(filepath.Join(workspace, config.DefaultRuntimeDir, "auth.json"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRunOAIRejectsExtraArguments(t *testing.T) {
	writeOAITestConfig(t, t.TempDir(), t.TempDir(), "api_key", "api_key", nil)
	stubDeviceLogin(t, "duplicate-flag-refresh")

	for _, args := range [][]string{
		{"login", "work", "extra"},
		{"login", "--headless", "--headless"},
		{"login", "--bad"},
		{"login", "--help", "--bad"},
		{"login", "--help", "work", "extra"},
		{"list", "extra"},
		{"logout", "work", "extra"},
		{"logout", "--bad"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			require.Error(t, runOAI(args))
		})
	}
}

func TestWriteAtomicReportsCommitState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	committed, err := writeAtomic(path, []byte("updated"), 0o640)
	require.NoError(t, err)
	assert.True(t, committed)

	committed, err = writeAtomic(filepath.Join(path, "child"), []byte("nope"), 0o640)
	assert.False(t, committed)
	assert.Error(t, err)
}

func TestRunOAIListReturnsMalformedAuthStoreError(t *testing.T) {
	workspace := t.TempDir()
	writeOAITestConfig(t, t.TempDir(), workspace, "chatgpt", "chatgpt", nil)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, config.DefaultRuntimeDir), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, config.DefaultRuntimeDir, "auth.json"), []byte(`{"digest_key":"missing-providers"}`), 0o600))

	err := runOAI([]string{"list"})
	require.ErrorContains(t, err, "malformed auth store")
}

func TestRunOAILoginHeadlessWrapsDeviceAuthorizationFailure(t *testing.T) {
	writeOAITestConfig(t, t.TempDir(), t.TempDir(), "api_key", "api_key", nil)
	base := http.DefaultClient.Transport
	t.Cleanup(func() { http.DefaultClient.Transport = base })
	http.DefaultClient.Transport = oaiLoginRoundTrip(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "https://auth.openai.com/api/accounts/deviceauth/usercode", req.URL.String())
		return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader("denied\n")), Header: make(http.Header)}, nil
	})

	err := runOAI([]string{"login", "--headless"})
	require.ErrorContains(t, err, "login with ChatGPT OAuth: device authorization failed (400): denied")
}

func writeOAITestConfig(t *testing.T, configDir, workspace, defaultAuth, namedAuth string, namedFields map[string]string, extra ...string) string {
	t.Helper()
	t.Chdir(configDir)

	named := map[string]string{
		"api_key":         "named-api-secret",
		"api_base_url":    "https://example.com/v1",
		"rocketcode_auth": namedAuth,
	}
	for key, value := range namedFields {
		named[key] = value
	}

	extraJSON := ""
	if len(extra) > 0 {
		extraJSON = extra[0]
	}

	data, err := json.Marshal(map[string]any{
		"workspace": workspace,
		"slack": map[string]any{
			"bot_token": "xoxb-test",
			"app_token": "xapp-test",
			"channels": []map[string]any{{
				"channel":          "#test",
				"agents":           []string{"main"},
				"allowed_user_ids": []string{"U123"},
			}},
		},
		"openai": map[string]string{
			"api_key":         "default-api-secret",
			"api_base_url":    "https://api.openai.com/v1",
			"rocketcode_auth": defaultAuth,
		},
		"providers": map[string]any{"work": named},
	})
	require.NoError(t, err)
	if extraJSON != "" {
		data = append(data[:len(data)-1], []byte(extraJSON+"}")...)
	}

	path := filepath.Join(configDir, defaultConfigPath)
	require.NoError(t, os.WriteFile(path, data, 0o640))
	return path
}

func stubDeviceLogin(t *testing.T, refresh string) {
	t.Helper()
	base := http.DefaultClient.Transport
	t.Cleanup(func() { http.DefaultClient.Transport = base })

	response := func(status int, body string) *http.Response {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
	}
	http.DefaultClient.Transport = oaiLoginRoundTrip(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://auth.openai.com/api/accounts/deviceauth/usercode":
			return response(http.StatusOK, `{"device_auth_id":"dev-123","user_code":"CODE-456","interval":"1"}`), nil
		case "https://auth.openai.com/api/accounts/deviceauth/token":
			return response(http.StatusOK, `{"authorization_code":"auth-code","code_verifier":"verifier"}`), nil
		case "https://auth.openai.com/oauth/token":
			return response(http.StatusOK, `{"access_token":"access-secret","refresh_token":"`+refresh+`","expires_in":60}`), nil
		default:
			t.Fatalf("unexpected OAuth request URL %s", req.URL)
			return nil, nil
		}
	})
}
