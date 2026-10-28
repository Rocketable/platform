package justbash

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func frontmatter(t *testing.T, doc string) map[string]any {
	t.Helper()

	var fm map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(doc), &fm))

	return fm
}

func TestParseSettings(t *testing.T) {
	t.Parallel()

	t.Run("not enabled", func(t *testing.T) {
		t.Parallel()

		_, enabled, err := ParseSettings(frontmatter(t, "model: gpt\n"))
		require.NoError(t, err)
		require.False(t, enabled)
	})

	t.Run("defaults", func(t *testing.T) {
		t.Parallel()

		got, enabled, err := ParseSettings(frontmatter(t, "shell: just-bash\n"))
		require.NoError(t, err)
		require.True(t, enabled)
		require.Equal(t, Settings{WorkingDirectory: "justbash/$ROCKETCLAW_CONVERSATION_ID", Commands: map[string]string{}, network: networkRules{auto: true}}, got)
	})

	t.Run("full example", func(t *testing.T) {
		t.Parallel()

		got, enabled, err := ParseSettings(frontmatter(t, `
shell: just-bash
workingDirectory: $PWD/somewhere/$ROCKETCLAW_CONVERSATION_ID
secretsARN:
  - arn:aws:secretsmanager:us-east-1:123456789012:secret:marketing-read
commands:
  stripe-write: "stripe -c $ROCKETCLAW_METADATA_CUSTOMER_ID"
  sudo: "bash -c --"
allowedNetworkTargets:
  - api.stripe.com
  - "*.googleapis.com"
  - 127.0.0.1:8080
  - 10.0.0.0/8
  - target: https://api.example.com/v1/
    methods: [get, POST]
`))
		require.NoError(t, err)
		require.True(t, enabled)
		require.Equal(t, Settings{
			WorkingDirectory: "$PWD/somewhere/$ROCKETCLAW_CONVERSATION_ID",
			Commands:         map[string]string{"stripe-write": "stripe -c $ROCKETCLAW_METADATA_CUSTOMER_ID", "sudo": "bash -c --"},
			SecretsARN:       []string{"arn:aws:secretsmanager:us-east-1:123456789012:secret:marketing-read"},
			network: networkRules{targets: []target{
				{host: "api.stripe.com"},
				{host: "*.googleapis.com"},
				{addr: netip.MustParseAddr("127.0.0.1"), port: "8080"},
				{prefix: netip.MustParsePrefix("10.0.0.0/8")},
				{host: "api.example.com", origin: "https://api.example.com", path: "/v1/", methods: []string{"GET", "POST"}},
			}},
		}, got)
	})

	errorCases := []struct {
		name, doc, want string
	}{
		{"customShell conflict", "shell: just-bash\ncustomShell: /bin/x\n", "customShell: cannot be combined with shell: just-bash"},
		{"key without shell", "commands: {a: b}\n", "commands: requires shell: just-bash"},
		{"other shell", "shell: zsh\n", "shell: must be just-bash"},
		{"empty folder", "shell: just-bash\nworkingDirectory: ''\n", "workingDirectory: must be a non-empty string"},
		{"commands list", "shell: just-bash\ncommands: [a]\n", "commands: must map command names to bash commands"},
		{"command with space", "shell: just-bash\ncommands: {'a b': x}\n", `commands: "a b" must name a command`},
		{"empty prefix", "shell: just-bash\ncommands: {a: ''}\n", `commands: "a" must name a command`},
		{"secrets string", "shell: just-bash\nsecretsARN: arn\n", "secretsARN: must be a list of strings"},
		{"network none", "shell: just-bash\nallowedNetworkTargets: none\n", "allowedNetworkTargets: must be auto or a list of targets"},
		{"ftp prefix", "shell: just-bash\nallowedNetworkTargets: ['ftp://x/']\n", `allowedNetworkTargets: "ftp://x/": URL prefixes must be http(s) URLs`},
		{"bad method", "shell: just-bash\nallowedNetworkTargets: [{target: a.com, methods: [FETCH]}]\n", `allowedNetworkTargets: methods of "a.com": unknown method "FETCH"`},
		{"bad cidr", "shell: just-bash\nallowedNetworkTargets: [10.0.0.0/33]\n", `allowedNetworkTargets: "10.0.0.0/33": not a host`},
		{"bad port", "shell: just-bash\nallowedNetworkTargets: ['a.com:99999']\n", `allowedNetworkTargets: "a.com:99999": invalid port`},
		{"ambiguous prefix", "shell: just-bash\nallowedNetworkTargets: ['https://a.com/v1/%2e%2e/']\n", `allowedNetworkTargets: "https://a.com/v1/%2e%2e/": ambiguous path`},
	}
	for _, tc := range errorCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := ParseSettings(frontmatter(t, tc.doc))
			require.ErrorContains(t, err, tc.want)
		})
	}
}
