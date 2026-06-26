package openresponsesd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadConfigFromArgsAppliesDocumentedOverrides(t *testing.T) {
	path := writeConfig(t, `{
	  "addr": "127.0.0.1:1000",
	  "auth": {"tokens": ["file-token"]},
	  "providers": {
	    "openai": {"type": "openai_responses", "api_key_env": "OPENRESPONSESD_OPENAI_API_KEY", "base_url": "https://api.openai.com/v1"}
	  },
	  "state": {"mode": "memory"}
	}`)

	cfg, err := loadConfigFromArgs([]string{"--addr", "127.0.0.1:9999", "--auth-token", "flag-token", "--provider", "openai"}, envMap(map[string]string{
		"OPENRESPONSESD_CONFIG":         path,
		"OPENRESPONSESD_AUTH_TOKEN":     "env-token",
		"OPENRESPONSESD_OPENAI_API_KEY": "env-openai-key",
	}))

	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:9999", cfg.Addr)
	require.Equal(t, []string{"flag-token"}, cfg.Auth.Tokens)
	require.Equal(t, "openai", cfg.DefaultProvider)
	require.Equal(t, "env-openai-key", cfg.Providers["openai"].APIKey)
}

func TestLoadConfigFromArgsRejectsOversizedConfig(t *testing.T) {
	path := writeConfig(t, strings.Repeat(" ", maxConfigBytes+1))

	_, err := loadConfigFromArgs([]string{"--config", path}, envMap(nil))

	require.ErrorContains(t, err, "exceeds 1048576 bytes")
}

func TestLoadConfigFromArgsReportsMissingConfigPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")

	_, err := loadConfigFromArgs([]string{"--config", path}, envMap(nil))

	require.ErrorContains(t, err, "read config "+path)
}

func TestLoadConfigFromArgsReportsInvalidJSON(t *testing.T) {
	path := writeConfig(t, `{`)

	_, err := loadConfigFromArgs([]string{"--config", path}, envMap(nil))

	require.ErrorContains(t, err, "parse config "+path)
}

func TestLoadConfigFromArgsRejectsTrailingJSON(t *testing.T) {
	path := writeConfig(t, `{"default_provider":"openai","providers":{"openai":{"type":"openai_responses","api_key":"key","base_url":"https://api.openai.com/v1"}}}{}`)

	_, err := loadConfigFromArgs([]string{"--config", path}, envMap(nil))

	require.ErrorContains(t, err, "trailing JSON content")
}

func TestLoadConfigFromArgsRejectsUnknownFields(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "top level", body: `{"default_provider":"openai","unexpected":true,"providers":{"openai":{"type":"openai_responses","api_key":"key","base_url":"https://api.openai.com/v1"}}}`},
		{name: "auth", body: `{"auth":{"token":"key"},"default_provider":"openai","providers":{"openai":{"type":"openai_responses","api_key":"key","base_url":"https://api.openai.com/v1"}}}`},
		{name: "provider", body: `{"default_provider":"openai","providers":{"openai":{"type":"openai_responses","api_key":"key","base_url":"https://api.openai.com/v1","secret":"nope"}}}`},
		{name: "route", body: `{"model_routes":[{"match":"gpt-*","provider":"openai","extra":true}],"providers":{"openai":{"type":"openai_responses","api_key":"key","base_url":"https://api.openai.com/v1"}}}`},
		{name: "state", body: `{"default_provider":"openai","providers":{"openai":{"type":"openai_responses","api_key":"key","base_url":"https://api.openai.com/v1"}},"state":{"mode":"memory","path":"state.db"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, tc.body)

			_, err := loadConfigFromArgs([]string{"--config", path}, envMap(nil))

			require.ErrorContains(t, err, "unknown field")
		})
	}
}

func TestConfigValidationRejectsBlankAuthTokens(t *testing.T) {
	path := writeConfig(t, `{
	  "auth": {"tokens": ["  "]},
	  "default_provider": "openai",
	  "providers": {
	    "openai": {"type": "openai_responses", "api_key": "key", "base_url": "https://api.openai.com/v1"}
	  }
	}`)

	_, err := loadConfigFromArgs([]string{"--config", path}, envMap(nil))

	require.ErrorContains(t, err, "auth.tokens[0] must not be empty")
}

func TestConfigValidationNormalizesAuthTokenOverrides(t *testing.T) {
	path := writeConfig(t, `{
	  "default_provider": "openai",
	  "providers": {
	    "openai": {"type": "openai_responses", "api_key": "key", "base_url": "https://api.openai.com/v1"}
	  }
	}`)

	cfg, err := loadConfigFromArgs([]string{"--config", path, "--auth-token", " token "}, envMap(nil))

	require.NoError(t, err)
	require.Equal(t, []string{"token"}, cfg.Auth.Tokens)
}

func TestConfigValidationRejectsUnknownProviderType(t *testing.T) {
	path := writeConfig(t, `{
	  "default_provider": "local",
	  "providers": {
	    "local": {"type": "bogus", "api_key": "key", "base_url": "http://127.0.0.1:8080/v1"}
	  }
	}`)

	_, err := loadConfigFromArgs([]string{"--config", path}, envMap(nil))

	require.ErrorContains(t, err, `providers.local.type "bogus" is unsupported`)
}

func TestConfigValidationRejectsMissingDefaultProvider(t *testing.T) {
	path := writeConfig(t, `{
	  "default_provider": "missing",
	  "providers": {
	    "openai": {"type": "openai_responses", "api_key": "key", "base_url": "https://api.openai.com/v1"}
	  }
	}`)

	_, err := loadConfigFromArgs([]string{"--config", path}, envMap(nil))

	require.ErrorContains(t, err, `default_provider "missing" is not defined in providers`)
}

func TestConfigValidationRequiresDeterministicRouting(t *testing.T) {
	path := writeConfig(t, `{
	  "providers": {
	    "openai": {"type": "openai_responses", "api_key": "key", "base_url": "https://api.openai.com/v1"}
	  }
	}`)

	_, err := loadConfigFromArgs([]string{"--config", path}, envMap(nil))

	require.ErrorContains(t, err, "default_provider or model_routes is required")
}

func TestConfigValidationRejectsInvalidProviderDefinitions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		wantErr string
	}{
		{name: "missing type", body: `{"default_provider":"local","providers":{"local":{"api_key":"key","base_url":"http://127.0.0.1:8080/v1"}}}`, wantErr: "providers.local.type is required"},
		{name: "missing base URL", body: `{"default_provider":"local","providers":{"local":{"type":"openai_chat_completions","api_key":"key"}}}`, wantErr: "providers.local.base_url is required"},
		{name: "missing credential", body: `{"default_provider":"local","providers":{"local":{"type":"openai_chat_completions","base_url":"http://127.0.0.1:8080/v1"}}}`, wantErr: "providers.local.api_key or api_key_env is required"},
		{name: "undocumented env", body: `{"default_provider":"local","providers":{"local":{"type":"openai_chat_completions","api_key_env":"LOCAL_KEY","base_url":"http://127.0.0.1:8080/v1"}}}`, wantErr: `providers.local.api_key_env "LOCAL_KEY" is not documented`},
		{name: "missing env value", body: `{"default_provider":"local","providers":{"local":{"type":"openai_chat_completions","api_key_env":"OPENRESPONSESD_OPENAI_API_KEY","base_url":"http://127.0.0.1:8080/v1"}}}`, wantErr: `providers.local.api_key_env "OPENRESPONSESD_OPENAI_API_KEY" is not set`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, tc.body)

			_, err := loadConfigFromArgs([]string{"--config", path}, envMap(nil))

			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestConfigValidationRejectsInvalidModelRoutes(t *testing.T) {
	path := writeConfig(t, `{
	  "model_routes": [{"match": "gpt-*", "provider": "missing"}],
	  "providers": {
	    "openai": {"type": "openai_responses", "api_key": "key", "base_url": "https://api.openai.com/v1"}
	  }
	}`)

	_, err := loadConfigFromArgs([]string{"--config", path}, envMap(nil))

	require.ErrorContains(t, err, `model_routes[0].provider "missing" is not defined in providers`)
}

func TestConfigValidationRejectsNegativeStateBounds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   string
		wantErr string
	}{
		{name: "max responses", state: `{"mode":"memory","max_responses":-1}`, wantErr: "state.max_responses must not be negative"},
		{name: "ttl", state: `{"mode":"memory","ttl_seconds":-1}`, wantErr: "state.ttl_seconds must not be negative"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, `{
			  "default_provider": "openai",
			  "providers": {"openai": {"type": "openai_responses", "api_key": "key", "base_url": "https://api.openai.com/v1"}},
			  "state": `+tc.state+`
			}`)

			_, err := loadConfigFromArgs([]string{"--config", path}, envMap(nil))

			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "openresponsesd.json")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	return path
}

func envMap(values map[string]string) func(string) string {
	return func(name string) string {
		return strings.TrimSpace(values[name])
	}
}
