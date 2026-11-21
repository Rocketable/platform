package main

import (
	"encoding/json"
	"errors"
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
	"golang.org/x/sys/unix"
)

type oaiLoginRoundTrip func(*http.Request) (*http.Response, error)

func (f oaiLoginRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func requireSaveToken(t *testing.T, workspace, runtimeDir, provider string, token oai.Token) {
	t.Helper()
	require.NoError(t, oai.SaveTokenIn(workspace, runtimeDir, provider, token))
}

func TestRunOAILoginHelpAliases(t *testing.T) {
	output := captureStdout(t, func() error { return runOAILogin([]string{"-h"}) })
	assert.Contains(t, output, "rocketclaw oai login [provider] [--headless]")
}

func TestRunOAILoginRejectsInvalidArgumentsBeforeHelp(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want string
	}{
		{args: []string{"--bad", "--help"}, want: `unknown flag "--bad"`},
		{args: []string{"--headless", "--headless", "help"}, want: "duplicate --headless"},
		{args: []string{"work", "openai", "-h"}, want: "more than one provider"},
		{args: []string{"--aws-secrets-manager-arn"}, want: "flag needs an argument: -aws-secrets-manager-arn"},
		{args: []string{"--aws-secrets-manager-arn=first", "--aws-secrets-manager-arn=second"}, want: "duplicate --aws-secrets-manager-arn"},
	} {
		err := runOAILogin(tt.args)
		require.ErrorContains(t, err, tt.want)
	}
	output := captureStdout(t, func() error { return runOAILogin([]string{"--headless", "--help", "--bad"}) })
	assert.Contains(t, output, "rocketclaw oai login")
}

func TestRunOAILoginTreatsBareHelpAsProvider(t *testing.T) {
	workspace := prepareOAIConfig(t)
	mockDeviceLogin(t, "help-refresh")
	synctest.Test(t, func(t *testing.T) {
		output := captureStdout(t, func() error { return runOAILogin([]string{"help", "--headless"}) })
		assert.Contains(t, output, "Saved help ChatGPT OAuth token")
	})
	token, err := oai.LoadTokenIn(workspace, config.DefaultRuntimeDir, "help")
	require.NoError(t, err)
	assert.Equal(t, "help-refresh", token.Refresh)
}

func TestRunOAILoginParsesHeadlessBeforeProvider(t *testing.T) {
	workspace := prepareOAIConfig(t)
	mockDeviceLogin(t, "work-refresh")

	synctest.Test(t, func(t *testing.T) {
		output := captureStdout(t, func() error { return runOAILogin([]string{"--headless", "work"}) })
		assert.Contains(t, output, "Saved work ChatGPT OAuth token")
	})

	token, err := oai.LoadTokenIn(workspace, config.DefaultRuntimeDir, "work")
	require.NoError(t, err)
	assert.Equal(t, "work-refresh", token.Refresh)
}

func TestRunOAILoginDefaultsProviderToOpenAI(t *testing.T) {
	workspace := prepareOAIConfig(t)
	mockDeviceLogin(t, "openai-refresh")

	synctest.Test(t, func(t *testing.T) {
		output := captureStdout(t, func() error { return runOAILogin([]string{"--headless"}) })
		assert.Contains(t, output, "Saved openai ChatGPT OAuth token")
	})

	_, err := oai.LoadTokenIn(workspace, config.DefaultRuntimeDir, "openai")
	require.NoError(t, err)
}

func TestRunOAILoginRejectsBeforeOAuth(t *testing.T) {
	for _, tt := range []struct {
		name, secret, provider, want string
	}{
		{name: "unknown provider", provider: "missing", want: `unknown provider "missing"`},
		{name: "unknown provider with secret", secret: `{"providers":{"other":{"api_key":"k"}}}`, provider: "missing", want: `unknown provider "missing"`},
		{name: "secret sets default api key", secret: `{"openai":{"auth":"api_key"}}`, provider: "openai", want: `the AWS secret sets provider "openai" auth to "api_key", which overrides the config file; set it to "chatgpt" in the secret`},
		{name: "secret sets named api key", secret: `{"providers":{"work":{"auth":" api_key "}}}`, provider: "work", want: `the AWS secret sets provider "work" auth to "api_key", which overrides the config file; set it to "chatgpt" in the secret`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			workspace := prepareOAIConfig(t)
			original, err := os.ReadFile(defaultConfigPath)
			require.NoError(t, err)
			args := []string{tt.provider, "--headless"}
			if tt.secret != "" {
				useTestSecrets(t, testSecrets{testSecretsARN: tt.secret})
				args = append(args, "--aws-secrets-manager-arn", testSecretsARN)
			}
			requests := 0
			base := http.DefaultClient.Transport
			http.DefaultClient.Transport = oaiLoginRoundTrip(func(*http.Request) (*http.Response, error) {
				requests++
				return nil, errors.New("OAuth must not start")
			})
			t.Cleanup(func() { http.DefaultClient.Transport = base })

			require.EqualError(t, runOAILogin(args), tt.want)
			assert.Zero(t, requests)
			data, err := os.ReadFile(defaultConfigPath)
			require.NoError(t, err)
			assert.Equal(t, original, data)
			_, err = oai.LoadTokenIn(workspace, config.DefaultRuntimeDir, tt.provider)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestRunOAILoginSetsUpChatGPT(t *testing.T) {
	for _, tt := range []struct {
		name, file, secret, provider, want string
	}{
		{name: "fresh config", file: `{"openai": {}}`, provider: "openai", want: `{"openai":{"auth":"chatgpt"}}`},
		{name: "secret without config file", secret: `{"openai":{"api_key":"k"}}`, provider: "openai", want: `{"openai":{"auth":"chatgpt"}}`},
		{name: "secret named provider without config file", secret: `{"providers":{"work":{"api_key":"k"}}}`, provider: "work", want: `{"providers":{"work":{"auth":"chatgpt"}}}`},
		{
			name:     "secret named provider missing from config file",
			file:     `{"future":true,"providers":{"alpha":{"api_key":"a"}}}`,
			secret:   `{"providers":{"work":{"api_key":"k"}}}`,
			provider: "work",
			want:     `{"future":true,"providers":{"alpha":{"api_key":"a"},"work":{"auth":"chatgpt"}}}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			t.Chdir(workspace)
			if tt.file != "" {
				require.NoError(t, os.WriteFile(defaultConfigPath, []byte(tt.file), 0o600))
			}
			args := []string{tt.provider, "--headless"}
			if tt.secret != "" {
				useTestSecrets(t, testSecrets{testSecretsARN: tt.secret})
				args = append(args, "--aws-secrets-manager-arn", testSecretsARN)
			}
			mockDeviceLogin(t, "refresh")

			synctest.Test(t, func(t *testing.T) {
				output := captureStdout(t, func() error { return runOAILogin(args) })
				assert.Contains(t, output, "Restart RocketClaw to use ChatGPT OAuth for "+tt.provider)
			})

			data, err := os.ReadFile(defaultConfigPath)
			require.NoError(t, err)
			assert.JSONEq(t, tt.want, string(data))
			info, err := os.Stat(defaultConfigPath)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
			token, err := oai.LoadTokenIn(workspace, config.DefaultRuntimeDir, tt.provider)
			require.NoError(t, err)
			assert.Equal(t, "refresh", token.Refresh)
		})
	}
}

func TestRunOAILoginLeavesConfigAloneWhenSecretUsesChatGPT(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	useTestSecrets(t, testSecrets{testSecretsARN: `{"providers":{"work":{"auth":"chatgpt"}}}`})
	mockDeviceLogin(t, "refresh")

	synctest.Test(t, func(t *testing.T) {
		output := captureStdout(t, func() error {
			return runOAILogin([]string{"work", "--headless", "--aws-secrets-manager-arn", testSecretsARN})
		})
		assert.NotContains(t, output, "Restart RocketClaw")
	})

	_, err := os.Stat(defaultConfigPath)
	require.ErrorIs(t, err, os.ErrNotExist)
	token, err := oai.LoadTokenIn(workspace, config.DefaultRuntimeDir, "work")
	require.NoError(t, err)
	assert.Equal(t, "refresh", token.Refresh)
}

func TestRunOAIListIgnoresServerOnlySettings(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile(defaultConfigPath, []byte(`{"openai": {}, "providers": {"work": {"auth": "chatgpt"}}}`), 0o600))

	output := captureStdout(t, func() error { return runOAI([]string{"list"}) })
	assert.Equal(t, "openai (default)\tapi_key\tmissing\nwork\tchatgpt\tmissing\n", output)
}

func TestRunOAILoginUpdatesOnlySelectedProvider(t *testing.T) {
	workspace := prepareOAIConfig(t)
	mockDeviceLogin(t, "work-refresh")

	synctest.Test(t, func(t *testing.T) {
		captureStdout(t, func() error { return runOAILogin([]string{"work", "--headless"}) })
	})

	data, err := os.ReadFile(defaultConfigPath)
	require.NoError(t, err)
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.JSONEq(t, `{"auth":"api_key","api_key":"openai-key"}`, string(raw["openai"]))
	assert.JSONEq(t, `{"auth":"chatgpt","api_key":"work-key","future_provider_field":true}`, providerJSON(t, raw, "work"))
	assert.JSONEq(t, `{"auth":"api_key","api_key":"alpha-key"}`, providerJSON(t, raw, "alpha"))
	assert.JSONEq(t, `{"enabled":true}`, string(raw["future_root_field"]))
	assert.Contains(t, string(data), `"workspace": "../runtime"`)
	assert.Contains(t, string(data), `"database_url": "postgres://localhost/rocketclaw_test?sslmode=disable"`)
	assert.Contains(t, string(data), `"future_large": 9007199254740993`)

	authPath, err := oai.AuthFilePathIn(workspace, config.DefaultRuntimeDir)
	require.NoError(t, err)
	_, err = os.Stat(authPath)
	require.NoError(t, err)
}

func TestSaveOAILoginRejectsConfigChangedDuringOAuth(t *testing.T) {
	workspace := prepareOAIConfig(t)
	for _, tt := range []struct {
		name string
		data string
		want string
	}{
		{name: "missing provider", data: `{"openai":{"auth":"api_key"},"providers":{}}`, want: "provider"},
		{name: "malformed providers", data: `{"openai":{"auth":"api_key"},"providers":[]}`, want: "provider"},
		{name: "malformed provider", data: `{"openai":{"auth":"api_key"},"providers":{"work":[]}}`, want: "provider"},
		{name: "malformed JSON", data: `{`, want: "parse config JSON"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(defaultConfigPath, []byte(tt.data), 0o640))
			err := saveOAILogin(defaultConfigPath, workspace, config.DefaultRuntimeDir, "work", false, oai.Token{Refresh: "refresh"})
			require.ErrorContains(t, err, tt.want)
			data, err := os.ReadFile(defaultConfigPath)
			require.NoError(t, err)
			assert.Equal(t, tt.data, string(data))
		})
	}
}

func TestSaveOAILoginReportsMissingConfigFolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "config.json")
	err := saveOAILogin(path, t.TempDir(), config.DefaultRuntimeDir, "work", true, oai.Token{Refresh: "refresh"})
	require.ErrorContains(t, err, "open config lock")
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestSaveOAILoginLeavesConfigAndCredentialsUnchangedWhenConfigCommitFails(t *testing.T) {
	workspace := prepareOAIConfig(t)
	original, err := os.ReadFile(defaultConfigPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(defaultConfigPath+".lock", nil, 0o600))
	require.NoError(t, os.Chmod(".", 0o500))
	t.Cleanup(func() { require.NoError(t, os.Chmod(".", 0o700)) })

	err = saveOAILogin(defaultConfigPath, workspace, config.DefaultRuntimeDir, "work", false, oai.Token{Refresh: "refresh"})
	require.ErrorContains(t, err, "commit config")
	require.ErrorIs(t, err, os.ErrPermission)
	data, err := os.ReadFile(defaultConfigPath)
	require.NoError(t, err)
	assert.Equal(t, original, data)
	_, err = oai.LoadTokenIn(workspace, config.DefaultRuntimeDir, "work")
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestSaveOAILoginPreservesFullFileMode(t *testing.T) {
	workspace := prepareOAIConfig(t)
	wantMode := os.FileMode(0o640) | os.ModeSetgid
	require.NoError(t, os.Chmod(defaultConfigPath, wantMode))
	err := saveOAILogin(defaultConfigPath, workspace, config.DefaultRuntimeDir, "work", false, oai.Token{Refresh: "refresh"})
	require.NoError(t, err)
	info, err := os.Stat(defaultConfigPath)
	require.NoError(t, err)
	assert.Equal(t, wantMode, info.Mode()&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky))
}

func TestRunOAILoginRestoresConfigWhenCredentialWriteFails(t *testing.T) {
	workspace := prepareOAIConfig(t)
	original, err := os.ReadFile(defaultConfigPath)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, config.DefaultRuntimeDir, "auth.json"), 0o700))
	mockDeviceLogin(t, "work-refresh")

	synctest.Test(t, func(t *testing.T) {
		err = runOAILogin([]string{"work", "--headless"})
	})
	require.ErrorContains(t, err, "OpenAI OAuth token")

	data, err := os.ReadFile(defaultConfigPath)
	require.NoError(t, err)
	assert.Equal(t, original, data)
}

func TestSaveOAILoginDoesNotRollbackCommittedToken(t *testing.T) {
	workspace := prepareOAIConfig(t)
	intended := oai.Token{Refresh: "refresh"}
	requireSaveToken(t, workspace, config.DefaultRuntimeDir, "work", intended)
	authPath, err := oai.AuthFilePathIn(workspace, config.DefaultRuntimeDir)
	require.NoError(t, err)
	require.NoError(t, os.Remove(authPath+".lock"))
	require.NoError(t, os.Mkdir(authPath+".lock", 0o700))
	err = saveOAILogin(defaultConfigPath, workspace, config.DefaultRuntimeDir, "work", false, intended)
	require.Error(t, err)
	stored, err := oai.LoadTokenIn(workspace, config.DefaultRuntimeDir, "work")
	require.NoError(t, err)
	assert.Equal(t, intended, stored)
	data, err := os.ReadFile(defaultConfigPath)
	require.NoError(t, err)
	assert.Contains(t, providerJSON(t, mustRawConfig(t, data), "work"), `"auth": "chatgpt"`)
}

func TestRunOAILoginSerializesDifferentProviderConfigChanges(t *testing.T) {
	workspace := prepareOAIConfig(t)
	selected, cfg, err := loadRuntimeConfig("", "")
	require.NoError(t, err)
	lock, err := os.OpenFile(selected.Path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	require.NoError(t, unix.Flock(int(lock.Fd()), unix.LOCK_EX))
	started := make(chan struct{}, 2)
	errs := make(chan error, 2)
	for _, provider := range []string{"openai", "work"} {
		go func() {
			started <- struct{}{}
			err := saveOAILogin(selected.Path, cfg.Workspace, cfg.RuntimeDirName(), provider, false, oai.Token{Refresh: provider + "-refresh"})
			errs <- err
		}()
	}
	<-started
	<-started
	probe, err := os.OpenFile(selected.Path+".lock", os.O_RDWR, 0)
	require.NoError(t, err)
	err = unix.Flock(int(probe.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	require.ErrorIs(t, err, unix.EWOULDBLOCK)
	require.NoError(t, probe.Close())
	require.NoError(t, unix.Flock(int(lock.Fd()), unix.LOCK_UN))
	require.NoError(t, lock.Close())
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)

	data, err := os.ReadFile(defaultConfigPath)
	require.NoError(t, err)
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Contains(t, string(raw["openai"]), `"auth": "chatgpt"`)
	assert.Contains(t, providerJSON(t, raw, "work"), `"auth": "chatgpt"`)
	for _, provider := range []string{"openai", "work"} {
		token, err := oai.LoadTokenIn(workspace, config.DefaultRuntimeDir, provider)
		require.NoError(t, err)
		assert.Equal(t, provider+"-refresh", token.Refresh)
	}
}

func mustRawConfig(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raw))
	return raw
}

func TestRunOAILoginPrintsRestartGuidanceOnlyWhenModeChanges(t *testing.T) {
	prepareOAIConfig(t)
	mockDeviceLogin(t, "work-refresh")
	synctest.Test(t, func(t *testing.T) {
		output := captureStdout(t, func() error { return runOAILogin([]string{"work", "--headless"}) })
		assert.Contains(t, output, "Restart RocketClaw")
		output = captureStdout(t, func() error { return runOAILogin([]string{"work", "--headless"}) })
		assert.NotContains(t, output, "Restart RocketClaw")
	})
}

func TestRunOAIListSortsProvidersWithoutCredentials(t *testing.T) {
	workspace := prepareOAIConfig(t)
	requireSaveToken(t, workspace, config.DefaultRuntimeDir, "work", oai.Token{Refresh: "secret-refresh", Access: "secret-access", AccountID: "secret-account"})

	output := captureStdout(t, func() error { return runOAI([]string{"list"}) })
	assert.Equal(t, "alpha\tapi_key\tmissing\nhelp\tapi_key\tmissing\nopenai (default)\tapi_key\tmissing\nwork\tapi_key\tpresent\n", output)
	assert.NotContains(t, output, "secret")
}

func TestRunOAILogoutRemovesOnlySelectedProvider(t *testing.T) {
	workspace := prepareOAIConfig(t)
	requireSaveToken(t, workspace, config.DefaultRuntimeDir, "openai", oai.Token{Refresh: "openai-refresh"})
	requireSaveToken(t, workspace, config.DefaultRuntimeDir, "work", oai.Token{Refresh: "work-refresh"})

	output := captureStdout(t, func() error { return runOAI([]string{"logout", "work"}) })
	assert.Contains(t, output, "Removed local ChatGPT OAuth token for work")

	_, err := oai.LoadTokenIn(workspace, config.DefaultRuntimeDir, "openai")
	require.NoError(t, err)
	_, err = oai.LoadTokenIn(workspace, config.DefaultRuntimeDir, "work")
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRunOAIWrapsRestartAndLogoutOutputErrors(t *testing.T) {
	t.Run("restart", func(t *testing.T) {
		prepareOAIConfig(t)
		mockDeviceLoginClosingStdout(t)
		synctest.Test(t, func(t *testing.T) {
			err := runOAILogin([]string{"work", "--headless"})
			require.ErrorContains(t, err, "print oai login result")
		})
	})
	t.Run("logout", func(t *testing.T) {
		workspace := prepareOAIConfig(t)
		requireSaveToken(t, workspace, config.DefaultRuntimeDir, "work", oai.Token{Refresh: "refresh"})
		err := runWithClosedStdout(t, func() error { return runOAILogout([]string{"work"}) })
		require.ErrorContains(t, err, "print oai logout result")
	})
}

func TestRunOAIHelpListsLoginListAndLogout(t *testing.T) {
	output := captureStdout(t, func() error { return runOAI(nil) })
	assert.Contains(t, output, "rocketclaw oai login [provider] [--headless]")
	assert.Contains(t, output, "rocketclaw oai list")
	assert.Contains(t, output, "rocketclaw oai logout [provider]")
}

func TestRunOAIRejectsInvalidArguments(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want string
	}{
		{args: []string{"list", "extra"}, want: "oai list takes no arguments"},
		{args: []string{"logout", "--bad"}, want: "parse oai logout flags"},
		{args: []string{"logout", "openai", "work"}, want: "more than one provider"},
	} {
		err := runOAI(tt.args)
		require.ErrorContains(t, err, tt.want)
	}
}

func prepareOAIConfig(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	workspace := filepath.Join(root, "runtime")
	require.NoError(t, os.Mkdir(configDir, 0o700))
	require.NoError(t, os.Mkdir(workspace, 0o700))
	t.Chdir(configDir)
	data := `{
  "workspace": "../runtime", "database_url": "postgres://localhost/rocketclaw_test?sslmode=disable",
  "openai": {"auth":"api_key","api_key":"openai-key"},
  "providers": {
    "work": {"auth":"api_key","api_key":"work-key","future_provider_field":true},
    "alpha": {"auth":"api_key","api_key":"alpha-key"},
    "help": {"auth":"api_key","api_key":"help-key"}
  },
	"slack": {"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},
  "web": {"tsnet": {}},
  "future_root_field": {"enabled":true},
  "future_large": 9007199254740993
}`
	require.NoError(t, os.WriteFile(defaultConfigPath, []byte(data), 0o640))
	return workspace
}

func runWithClosedStdout(t *testing.T, run func() error) error {
	t.Helper()
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.NoError(t, writer.Close())
	stdout := os.Stdout
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = stdout })
	return run()
}

func mockDeviceLogin(t *testing.T, refresh string) {
	t.Helper()
	base := http.DefaultClient.Transport
	http.DefaultClient.Transport = oaiLoginRoundTrip(func(req *http.Request) (*http.Response, error) {
		body := ""
		switch req.URL.String() {
		case "https://auth.openai.com/api/accounts/deviceauth/usercode":
			body = `{"device_auth_id":"dev-123","user_code":"CODE-456","interval":"1"}`
		case "https://auth.openai.com/api/accounts/deviceauth/token":
			body = `{"authorization_code":"auth-code","code_verifier":"verifier"}`
		case "https://auth.openai.com/oauth/token":
			body = `{"access_token":"access","refresh_token":"` + refresh + `","expires_in":60}`
		default:
			t.Fatalf("unexpected OAuth request URL %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	t.Cleanup(func() { http.DefaultClient.Transport = base })
}

func mockDeviceLoginClosingStdout(t *testing.T) {
	t.Helper()
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	stdout := os.Stdout
	os.Stdout = writer
	t.Cleanup(func() {
		os.Stdout = stdout
		_ = reader.Close()
		_ = writer.Close()
	})
	base := http.DefaultClient.Transport
	http.DefaultClient.Transport = oaiLoginRoundTrip(func(req *http.Request) (*http.Response, error) {
		body := `{"device_auth_id":"dev-123","user_code":"CODE-456","interval":"1"}`
		if strings.HasSuffix(req.URL.Path, "/deviceauth/token") {
			body = `{"authorization_code":"auth-code","code_verifier":"verifier"}`
		} else if strings.HasSuffix(req.URL.Path, "/oauth/token") {
			require.NoError(t, writer.Close())
			body = `{"access_token":"access","refresh_token":"work-refresh","expires_in":60}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	t.Cleanup(func() { http.DefaultClient.Transport = base })
}

func providerJSON(t *testing.T, raw map[string]json.RawMessage, provider string) string {
	t.Helper()
	var providers map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw["providers"], &providers))
	return string(providers[provider])
}
