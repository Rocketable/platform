package config

import (
	"errors"
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

func TestSecretFieldReturnsKey(t *testing.T) {
	got, err := secretField(&recordingSecrets{body: `{"token":"alpha"}`}, testSecretARN, "token")
	require.NoError(t, err)
	assert.Equal(t, "alpha", got)
}

func TestSecretFieldMissingKeyOmitsSecretBody(t *testing.T) {
	_, err := secretField(&recordingSecrets{body: `{"token":"LEAKED-SECRET-VALUE"}`}, testSecretARN, "missing")
	require.Error(t, err)
	require.ErrorContains(t, err, testSecretARN)
	require.ErrorContains(t, err, "missing")
	assert.NotContains(t, err.Error(), "LEAKED-SECRET-VALUE")
}

func TestSecretFieldFetchesSameARNTwice(t *testing.T) {
	fetcher := &recordingSecrets{body: `{"token":"alpha"}`}
	_, err := secretField(fetcher, testSecretARN, "token")
	require.NoError(t, err)
	_, err = secretField(fetcher, testSecretARN, "token")
	require.NoError(t, err)
	assert.Equal(t, 2, fetcher.calls)
}

func TestSecretsManagerRegionRejectsIncompleteARN(t *testing.T) {
	_, err := secretsManagerRegion("femto")
	require.Error(t, err)
	require.ErrorContains(t, err, "femto")
	fetchErr, ok := errors.AsType[*SecretFetchError](err)
	require.True(t, ok)
	assert.Equal(t, "femto", fetchErr.ARN)
}

func TestAWSFetcherRejectsIncompleteARN(t *testing.T) {
	_, err := AWSFetcher{}.SecretString("femto")
	require.Error(t, err)
	require.ErrorContains(t, err, "femto")
	_, ok := errors.AsType[*SecretFetchError](err)
	assert.True(t, ok)
}

func TestSecretFetchErrorUnwraps(t *testing.T) {
	err := &SecretFetchError{ARN: testSecretARN, Key: "token", err: errSecretKeyMissing}
	require.ErrorIs(t, err, errSecretKeyMissing)
}

func TestSecretFieldRejectsNonObjectAndNonString(t *testing.T) {
	_, err := secretField(&recordingSecrets{body: `[1]`}, testSecretARN, "token")
	require.ErrorIs(t, err, errSecretNotJSON)
	_, err = secretField(&recordingSecrets{body: `{"token":1}`}, testSecretARN, "token")
	require.ErrorIs(t, err, errSecretKeyNotString)
}

func TestSecretsManagerRegionRejectsWrongService(t *testing.T) {
	_, err := secretsManagerRegion("arn:aws:s3:us-east-1:123456789012:secret:femto")
	require.ErrorIs(t, err, errIncompleteARN)
}
