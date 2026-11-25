package harness

import (
	"cmp"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestTurnSecretStoreSave(t *testing.T) {
	const value = "ghp_abcd"

	for _, tc := range []struct {
		name, value string
		accepted    bool
	}{
		{name: "GITHUB_TOKEN", value: value, accepted: true},
		{name: "AWS_SECRET_ACCESS_KEY", value: value, accepted: true},
		{name: "DB_PASSWORD", value: value, accepted: true},
		{name: "SIGNING_KEY", value: value, accepted: true},
		{name: strings.Repeat("A", 58) + "_TOKEN", value: value, accepted: true},
		{name: strings.Repeat("A", 59) + "_TOKEN", value: value},
		{name: "github_token", value: value},
		{name: "PATH", value: value},
		{name: "PS4", value: value},
		{name: "SHELLOPTS", value: value},
		{name: "NODE_OPTIONS", value: value},
		{name: "GIT_SSH_COMMAND", value: value},
		{name: "PYTHONPATH", value: value},
		{name: "DATABASE_URL", value: value},
		{name: "_TOKEN", value: value},
		{name: "GITHUB_TOKEN", value: ""},
		{name: "GITHUB_TOKEN", value: "ghp_abc"},
		{name: "GITHUB_TOKEN", value: "ghp\x00abcd"},
		{name: "GITHUB_TOKEN", value: "ghp_abcd\n"},
		{name: "GITHUB_TOKEN", value: "ghp_\rabcd"},
		{name: "GITHUB_TOKEN", value: strings.Repeat("a", 64<<10), accepted: true},
		{name: "GITHUB_TOKEN", value: strings.Repeat("a", 64<<10+1)},
	} {
		store := NewTurnSecretStore()

		err := store.Save(tc.name, []byte(tc.value))
		if tc.accepted {
			require.NoError(t, err, tc.name)
			require.True(t, store.Holds(tc.name), tc.name)
			require.Equal(t, []string{tc.name + "=" + tc.value}, store.Env(), tc.name)

			continue
		}

		errSecret, ok := errors.AsType[*TurnSecretError](err)
		require.True(t, ok, "%q %q: %v", tc.name, tc.value, err)
		require.Equal(t, tc.name, errSecret.Name)
		require.NotEmpty(t, errSecret.Problem)
		require.NotContains(t, err.Error(), cmp.Or(tc.value, "\x00"))
		require.False(t, store.Holds(tc.name))
		require.Empty(t, store.Env())
	}
}

func TestTurnSecretStoreEnv(t *testing.T) {
	store := NewTurnSecretStore()
	pasted := []byte("ghp_first-value")
	require.NoError(t, store.Save("GITHUB_TOKEN", pasted))

	clear(pasted)
	require.NoError(t, store.Save("AWS_SECRET_ACCESS_KEY", []byte("aws-secret-value")))
	require.Equal(t, []string{"AWS_SECRET_ACCESS_KEY=aws-secret-value", "GITHUB_TOKEN=ghp_first-value"}, store.Env())

	require.NoError(t, store.Save("GITHUB_TOKEN", []byte("ghp_second-value")))
	require.Equal(t, []string{"AWS_SECRET_ACCESS_KEY=aws-secret-value", "GITHUB_TOKEN=ghp_second-value"}, store.Env())
	require.Equal(t, "[secret:GITHUB_TOKEN] [secret:GITHUB_TOKEN]", store.Redact("ghp_first-value ghp_second-value"))
	require.False(t, store.Holds("DB_PASSWORD"))
}

func TestTurnSecretStoreRedact(t *testing.T) {
	store := NewTurnSecretStore()
	require.NoError(t, store.Save("GITHUB_TOKEN", []byte("ghp_abcdefgh")))
	require.NoError(t, store.Save("OUTER_TOKEN", []byte("xx-ghp_abcdefgh-yy")))

	var secrets TurnSecrets = store

	require.Equal(t,
		"a [secret:GITHUB_TOKEN] b [secret:OUTER_TOKEN] c [secret:GITHUB_TOKEN][secret:GITHUB_TOKEN]",
		secrets.Redact("a ghp_abcdefgh b xx-ghp_abcdefgh-yy c ghp_abcdefghghp_abcdefgh"))
	require.Equal(t, "nothing to hide", secrets.Redact("nothing to hide"))
}

func TestTurnSecretStoreErase(t *testing.T) {
	store := NewTurnSecretStore()
	require.NoError(t, store.Save("GITHUB_TOKEN", []byte("ghp_abcdefgh")))
	require.NoError(t, store.Save("DB_PASSWORD", []byte("hunter2-hunter2")))
	require.NoError(t, store.Save("DB_PASSWORD", []byte("hunter3-hunter3")))
	held := slices.Concat(store.values["GITHUB_TOKEN"], store.values["DB_PASSWORD"])

	store.Erase()

	require.Empty(t, store.Env())
	require.False(t, store.Holds("GITHUB_TOKEN"))
	require.Equal(t, "ghp_abcdefgh hunter2-hunter2", store.Redact("ghp_abcdefgh hunter2-hunter2"))
	require.Equal(t, [][]byte{make([]byte, 12), make([]byte, 15), make([]byte, 15)}, held)
}

func TestTurnSecretStoreConcurrentUse(t *testing.T) {
	store := NewTurnSecretStore()

	var group errgroup.Group
	for range 4 {
		group.Go(func() error {
			errSave := store.Save("GITHUB_TOKEN", []byte("ghp_abcdefgh"))
			assert.Equal(t, []string{"GITHUB_TOKEN=ghp_abcdefgh"}, store.Env())
			assert.Equal(t, "[secret:GITHUB_TOKEN]", store.Redact("ghp_abcdefgh"))

			return errSave
		})
	}

	require.NoError(t, group.Wait())
	store.Erase()
	require.Empty(t, store.Env())
}

func TestInertTurnSecrets(t *testing.T) {
	var secrets TurnSecrets = InertTurnSecrets{}

	require.Empty(t, secrets.Env())
	require.Equal(t, "ghp_abcdefgh", secrets.Redact("ghp_abcdefgh"))
}
