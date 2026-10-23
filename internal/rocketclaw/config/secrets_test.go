package config

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

func (r *recordingSecrets) SecretString(string) (string, error) {
	r.calls++
	return r.body, r.err
}

func TestSecretFieldFetchesSameARNTwice(t *testing.T) {
	fetcher := &recordingSecrets{body: `{"token":"alpha"}`}
	_, err := secretField(fetcher, testSecretARN, "token")
	require.NoError(t, err)
	_, err = secretField(fetcher, testSecretARN, "token")
	require.NoError(t, err)
	assert.Equal(t, 2, fetcher.calls)
}

func TestAWSFetcherRejectsIncompleteARN(t *testing.T) {
	_, err := AWSFetcher{}.SecretString("femto")
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

			got, err := AWSFetcher{}.SecretString(testSecretARN)
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

func TestSecretFieldRejectsNonObjectAndNonString(t *testing.T) {
	_, err := secretField(&recordingSecrets{body: `[1]`}, testSecretARN, "token")
	require.ErrorIs(t, err, errSecretNotJSON)
	_, err = secretField(&recordingSecrets{body: `{"token":1}`}, testSecretARN, "token")
	require.ErrorIs(t, err, errSecretKeyNotString)
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
