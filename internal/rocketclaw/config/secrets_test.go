package config

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecretARN = "arn:aws:secretsmanager:us-east-1:123456789012:secret:femto"

type recordingSecrets struct {
	body  string
	calls int
	err   error
}

func (r *recordingSecrets) SecretString(context.Context, string) (string, error) {
	r.calls++
	return r.body, r.err
}

func TestSecretFieldFetchesSameARNTwice(t *testing.T) {
	fetcher := &recordingSecrets{body: `{"token":"alpha"}`}
	_, err := secretField(t.Context(), fetcher, testSecretARN, "token")
	require.NoError(t, err)
	_, err = secretField(t.Context(), fetcher, testSecretARN, "token")
	require.NoError(t, err)
	assert.Equal(t, 2, fetcher.calls)
}

func TestAWSFetcherRejectsIncompleteARN(t *testing.T) {
	_, err := new(AWSFetcher).SecretString(t.Context(), "femto")
	require.Error(t, err)
	require.ErrorContains(t, err, "femto")
	_, ok := errors.AsType[*SecretFetchError](err)
	assert.True(t, ok)
}

func TestAWSFetcherReadsSecretString(t *testing.T) {
	for _, tt := range []struct {
		name, body, want string
		status           int
		errWant          error
	}{
		{name: "secret", body: `{"SecretString":"alpha"}`, want: "alpha"},
		{name: "empty", body: `{"SecretString":""}`, errWant: errSecretEmpty},
		{name: "missing", status: http.StatusBadRequest, body: `{"__type":"ResourceNotFoundException","message":"gone"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "secretsmanager.GetSecretValue", r.Header.Get("X-Amz-Target"))
				w.Header().Set("Content-Type", "application/x-amz-json-1.1")

				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}

				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)
			home := t.TempDir()
			t.Setenv("AWS_CONFIG_FILE", filepath.Join(home, "config"))
			t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(home, "credentials"))
			t.Setenv("AWS_PROFILE", "")
			t.Setenv("AWS_ACCESS_KEY_ID", "test")
			t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
			t.Setenv("AWS_ENDPOINT_URL", server.URL)

			got, err := new(AWSFetcher).SecretString(t.Context(), testSecretARN)
			if tt.want != "" {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)

				return
			}

			fetchErr, ok := errors.AsType[*SecretFetchError](err)
			require.True(t, ok, "%v", err)
			assert.Equal(t, testSecretARN, fetchErr.ARN)

			if tt.errWant != nil {
				require.ErrorIs(t, err, tt.errWant)
			} else {
				require.ErrorContains(t, err, "ResourceNotFoundException")
			}
		})
	}
}

// One fetcher reuses its connection and credentials across fetches, as an easy agent's turns fetch each time,
// and a fetch stops with its turn's context.
func TestAWSFetcherReusesConnectionAndStopsWithContext(t *testing.T) {
	var (
		mu      sync.Mutex
		clients = map[string]bool{}
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		clients[r.RemoteAddr] = true
		mu.Unlock()

		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_, _ = w.Write([]byte(`{"SecretString":"alpha"}`))
	}))
	t.Cleanup(server.Close)
	home := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(home, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(home, "credentials"))
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_ENDPOINT_URL", server.URL)

	fetcher := new(AWSFetcher)
	for range 2 {
		got, err := fetcher.SecretString(t.Context(), testSecretARN)
		require.NoError(t, err)
		require.Equal(t, "alpha", got)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := fetcher.SecretString(ctx, testSecretARN)
	require.ErrorIs(t, err, context.Canceled)

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, clients, 1, "both fetches use one connection")
}

func TestSecretFieldRejectsNonObjectAndNonString(t *testing.T) {
	_, err := secretField(t.Context(), &recordingSecrets{body: `[1]`}, testSecretARN, "token")
	require.ErrorIs(t, err, errSecretNotJSON)
	_, err = secretField(t.Context(), &recordingSecrets{body: `{"token":1}`}, testSecretARN, "token")
	require.ErrorIs(t, err, errSecretKeyNotString)
}

// Each key of each secret is one variable. A bad secret fails with its ARN and key, never its value.
func TestSecretEnv(t *testing.T) {
	env, err := SecretEnv(t.Context(), &recordingSecrets{body: `{"TOKEN":"alpha","_b2":"beta"}`}, []string{testSecretARN})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"TOKEN": "alpha", "_b2": "beta"}, env)

	env, err = SecretEnv(t.Context(), &recordingSecrets{}, nil)
	require.NoError(t, err)
	assert.Empty(t, env, "no ARNs, no fetch")

	for _, tt := range []struct {
		name, body, key string
		arns            []string
		errWant         error
	}{
		{"not an object", `["hidden"]`, "", []string{testSecretARN}, errSecretNotJSON},
		{"not a string", `{"TOKEN":["hidden"]}`, "TOKEN", []string{testSecretARN}, errSecretKeyNotString},
		{"not a variable name", `{"1-TOKEN":"hidden"}`, "1-TOKEN", []string{testSecretARN}, errSecretKeyName},
		{"in two secrets", `{"TOKEN":"hidden"}`, "TOKEN", []string{testSecretARN, testSecretARN + "-2"}, errSecretKeyTwice},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := SecretEnv(t.Context(), &recordingSecrets{body: tt.body}, tt.arns)
			require.ErrorIs(t, err, tt.errWant)

			fetchErr, ok := errors.AsType[*SecretFetchError](err)
			require.True(t, ok, "%v", err)
			assert.Equal(t, tt.arns[len(tt.arns)-1], fetchErr.ARN)
			assert.Equal(t, tt.key, fetchErr.Key)
			assert.NotContains(t, err.Error(), "hidden")
		})
	}
}

func TestSecretsManagerRegion(t *testing.T) {
	for _, tt := range []struct {
		name, arn, region string
	}{
		{"wrong service", "arn:aws:s3:us-east-1:123456789012:secret:femto", ""},
		{"complete ARN", "arn:aws:secretsmanager:eu-west-1:123456789012:secret:femto", "eu-west-1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			region, err := secretsManagerRegion(tt.arn)
			if tt.region == "" {
				require.ErrorIs(t, err, errIncompleteARN)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.region, region)
		})
	}
}
