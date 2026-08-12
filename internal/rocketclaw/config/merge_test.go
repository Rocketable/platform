package config

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	flagARN  = "arn:aws:secretsmanager:us-east-1:123456789012:secret:femto"
	otherARN = "arn:aws:secretsmanager:us-west-2:123456789012:secret:other"
)

type mapSecrets map[string]string

func (m mapSecrets) SecretString(arn string) (string, error) {
	body, ok := m[arn]
	if !ok {
		return "", &SecretFetchError{ARN: arn, err: errSecretEmpty}
	}

	return body, nil
}

type countingSecrets struct {
	mapSecrets

	calls []string
}

func (c *countingSecrets) SecretString(arn string) (string, error) {
	c.calls = append(c.calls, arn)
	return c.mapSecrets.SecretString(arn)
}

func slackJSON(botToken string) string {
	return `{
	  "workspace": ".",
	  "openai": {"api_key": "test-key"},
	  "slack": {
	    "bot_token": ` + strconv.Quote(botToken) + `,
	    "app_token": "xapp-test",
	    "channels": [{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]
	  }
	}`
}

func loadSecretConfig(t *testing.T, local, arn string, fetcher SecretFetcher) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rocketclaw.json")
	require.NoError(t, os.WriteFile(path, []byte(local), 0o600))
	cfg, err := Load(path, arn, fetcher)
	require.NoError(t, err)

	return cfg
}

func TestLoadSecretStringBeatsLocalString(t *testing.T) {
	cfg := loadSecretConfig(t, slackJSON("xoxb-local"), flagARN, mapSecrets{
		flagARN: `{"slack":{"bot_token":"xoxb-secret"}}`,
	})
	assert.Equal(t, "xoxb-secret", cfg.Slack.BotToken)
}

func TestLoadKeepsLocalOnlyMCPServer(t *testing.T) {
	local := `{
	  "workspace": ".",
	  "openai": {"api_key": "test-key"},
	  "slack": {"bot_token":"xoxb-local","app_token":"xapp-test","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},
	  "mcp_servers": {"acme": {"url": "https://acme.example"}}
	}`
	cfg := loadSecretConfig(t, local, flagARN, mapSecrets{
		flagARN: `{"slack":{"bot_token":"xoxb-secret"}}`,
	})
	require.Contains(t, cfg.MCPServers, "acme")
	assert.Equal(t, "https://acme.example", cfg.MCPServers["acme"].URL)
	assert.Equal(t, "xoxb-secret", cfg.Slack.BotToken)
}

func TestLoadAddsSecretOnlyBotToken(t *testing.T) {
	local := `{
	  "workspace": ".",
	  "openai": {"api_key": "test-key"},
	  "slack": {"app_token":"xapp-test","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]}
	}`
	cfg := loadSecretConfig(t, local, flagARN, mapSecrets{
		flagARN: `{"slack":{"bot_token":"xoxb-secret"}}`,
	})
	assert.Equal(t, "xoxb-secret", cfg.Slack.BotToken)
}

func TestLoadSecretStringSkipsLocalAWSObject(t *testing.T) {
	local := `{
	  "workspace": ".",
	  "openai": {"api_key": "test-key"},
	  "slack": {
	    "bot_token": {"aws":{"arn":"` + otherARN + `","key":"token"}},
	    "app_token": "xapp-test",
	    "channels": [{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]
	  }
	}`
	fetcher := &countingSecrets{mapSecrets: mapSecrets{
		flagARN:  `{"slack":{"bot_token":"xoxb-secret"}}`,
		otherARN: `{"token":"should-not-fetch"}`,
	}}
	cfg := loadSecretConfig(t, local, flagARN, fetcher)
	assert.Equal(t, "xoxb-secret", cfg.Slack.BotToken)
	assert.Equal(t, []string{flagARN}, fetcher.calls)
}

func TestLoadFetchesAWSObjectFromSecret(t *testing.T) {
	local := `{
	  "workspace": ".",
	  "openai": {"api_key": "test-key"},
	  "slack": {"bot_token":"xoxb-local","app_token":"xapp-test","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},
	  "mcp_servers": {"acme": {"url": "https://acme.example"}}
	}`
	cfg := loadSecretConfig(t, local, flagARN, mapSecrets{
		flagARN:  `{"mcp_servers":{"acme":{"headers":{"Authorization":{"aws":{"arn":"` + otherARN + `","key":"token"}}}}}}`,
		otherARN: `{"token":"hdr-secret"}`,
	})
	assert.Equal(t, "hdr-secret", cfg.MCPServers["acme"].Headers["Authorization"])
	assert.Equal(t, "https://acme.example", cfg.MCPServers["acme"].URL)
}

func TestLoadMissingSecretKeyFailsClosed(t *testing.T) {
	local := `{
	  "workspace": ".",
	  "openai": {"api_key": "test-key"},
	  "slack": {
	    "bot_token": {"aws":{"arn":"` + otherARN + `","key":"missing"}},
	    "app_token": "xapp-test",
	    "channels": [{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]
	  }
	}`
	path := filepath.Join(t.TempDir(), "rocketclaw.json")
	require.NoError(t, os.WriteFile(path, []byte(local), 0o600))
	_, err := Load(path, "", mapSecrets{otherARN: `{"token":"LEAKED-SECRET-VALUE"}`})
	require.Error(t, err)
	require.ErrorContains(t, err, otherARN)
	require.ErrorContains(t, err, "missing")
	assert.NotContains(t, err.Error(), "LEAKED-SECRET-VALUE")
}

func TestLoadFetchesLocalAWSObjectWithoutFlag(t *testing.T) {
	local := `{
	  "workspace": ".",
	  "openai": {"api_key": "test-key"},
	  "slack": {"bot_token":"xoxb-local","app_token":"xapp-test","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},
	  "mcp_servers": {"acme": {"url": "https://acme.example", "headers": {"Authorization": {"aws":{"arn":"` + otherARN + `","key":"token"}}}}}
	}`
	cfg := loadSecretConfig(t, local, "", mapSecrets{
		otherARN: `{"token":"hdr-secret"}`,
	})
	assert.Equal(t, "hdr-secret", cfg.MCPServers["acme"].Headers["Authorization"])
}

func TestLoadListsReplaceObjectsMerge(t *testing.T) {
	local := `{
	  "workspace": ".",
	  "openai": {"api_key": "test-key"},
	  "slack": {
	    "bot_token": "xoxb-local",
	    "app_token": "xapp-test",
	    "channels": [
	      {"channel":"#one","agents":["main"],"allowed_user_ids":["U1"]},
	      {"channel":"#two","agents":["main"],"allowed_user_ids":["U2"]},
	      {"channel":"#three","agents":["main"],"allowed_user_ids":["U3"]}
	    ]
	  },
	  "mcp_servers": {"acme": {"url": "https://acme.example"}}
	}`
	cfg := loadSecretConfig(t, local, flagARN, mapSecrets{
		flagARN: `{"slack":{"channels":[{"channel":"#vault","agents":["main"],"allowed_user_ids":["U9"]}]},"mcp_servers":{"acme":{"headers":{"Authorization":"tok"}}}}`,
	})
	require.Len(t, cfg.Slack.Channels, 1)
	assert.Equal(t, "#vault", cfg.Slack.Channels[0].Channel)
	assert.Equal(t, "https://acme.example", cfg.MCPServers["acme"].URL)
	assert.Equal(t, "tok", cfg.MCPServers["acme"].Headers["Authorization"])
}

func TestLoadRejectsSecretThatIsNotAnObject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rocketclaw.json")
	require.NoError(t, os.WriteFile(path, []byte(slackJSON("xoxb-local")), 0o600))
	_, err := Load(path, flagARN, mapSecrets{flagARN: `["nope"]`})
	require.ErrorContains(t, err, "parse secret JSON")
}

func TestLoadRejectsMalformedAWSObject(t *testing.T) {
	local := `{
	  "workspace": ".",
	  "openai": {"api_key": "test-key"},
	  "slack": {
	    "bot_token": {"aws": "nope"},
	    "app_token": "xapp-test",
	    "channels": [{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]
	  }
	}`
	path := filepath.Join(t.TempDir(), "rocketclaw.json")
	require.NoError(t, os.WriteFile(path, []byte(local), 0o600))
	_, err := Load(path, "", mapSecrets{})
	require.ErrorIs(t, err, errInvalidAWSRef)
}

func TestDecodeObjectRejectsArray(t *testing.T) {
	_, err := decodeObject([]byte(`[]`), "config")
	require.ErrorContains(t, err, "must be an object")
}

func TestLoadEmptyLocalBotTokenDoesNotBeatSecret(t *testing.T) {
	cfg := loadSecretConfig(t, slackJSON(""), flagARN, mapSecrets{
		flagARN: `{"slack":{"bot_token":"xoxb-secret"}}`,
	})
	assert.Equal(t, "xoxb-secret", cfg.Slack.BotToken)
}
