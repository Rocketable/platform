package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

var (
	errIncompleteARN      = errors.New("must be a complete secretsmanager ARN")
	errSecretKeyMissing   = errors.New("key not found")
	errSecretKeyNotString = errors.New("key is not a string")
	errSecretKeyName      = errors.New("key is not an environment variable name")
	errSecretKeyTwice     = errors.New("key is in an earlier secret too")
	errSecretNotJSON      = errors.New("secret is not a JSON object")
	errSecretEmpty        = errors.New("secret string is empty")

	envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// SecretFetcher returns a Secrets Manager SecretString.
type SecretFetcher interface {
	SecretString(ctx context.Context, arn string) (string, error)
}

// SecretFetchError names the ARN and key of a failed fetch. It never includes the secret body.
type SecretFetchError struct {
	ARN string
	Key string
	err error
}

func (e *SecretFetchError) Error() string {
	if e.Key != "" {
		return fmt.Sprintf("secret %s key %s: %v", e.ARN, e.Key, e.err)
	}

	return fmt.Sprintf("secret %s: %v", e.ARN, e.err)
}

func (e *SecretFetchError) Unwrap() error { return e.err }

// AWSFetcher loads SecretString with the default AWS credential chain. It keeps one client per region, the
// chain's config loaded for that region, so fetches reuse its connections and cached credentials.
type AWSFetcher struct {
	clients sync.Map // Region to *secretsmanager.Client.
}

// SecretString fetches the current SecretString for arn.
func (f *AWSFetcher) SecretString(ctx context.Context, arn string) (string, error) {
	region, err := secretsManagerRegion(arn)
	if err != nil {
		return "", err
	}

	client, ok := f.clients.Load(region)
	if !ok {
		cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
		if err != nil {
			return "", &SecretFetchError{ARN: arn, err: err}
		}

		client, _ = f.clients.LoadOrStore(region, secretsmanager.NewFromConfig(cfg))
	}

	out, err := client.(*secretsmanager.Client).GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: &arn,
	})
	if err != nil {
		return "", &SecretFetchError{ARN: arn, err: err}
	}

	if out.SecretString == nil || *out.SecretString == "" {
		return "", &SecretFetchError{ARN: arn, err: errSecretEmpty}
	}

	return *out.SecretString, nil
}

func secretsManagerRegion(arn string) (string, error) {
	parts := strings.SplitN(arn, ":", 7)
	if len(parts) < 7 || parts[0] != "arn" || parts[2] != "secretsmanager" || parts[3] == "" || parts[5] != "secret" {
		return "", &SecretFetchError{ARN: arn, err: errIncompleteARN}
	}

	return parts[3], nil
}

func secretField(ctx context.Context, fetcher SecretFetcher, arn, key string) (string, error) {
	obj, err := secretObject(ctx, fetcher, arn)
	if err != nil {
		return "", err
	}

	value, ok := obj[key]
	if !ok {
		return "", &SecretFetchError{ARN: arn, Key: key, err: errSecretKeyMissing}
	}

	text, ok := value.(string)
	if !ok {
		return "", &SecretFetchError{ARN: arn, Key: key, err: errSecretKeyNotString}
	}

	return text, nil
}

// SecretEnv fetches every secret in arns as environment variables: each holds a JSON object whose keys are
// variable names and whose values are strings, and a key may appear in one secret only.
func SecretEnv(ctx context.Context, fetcher SecretFetcher, arns []string) (map[string]string, error) {
	env := map[string]string{}

	for _, arn := range arns {
		obj, err := secretObject(ctx, fetcher, arn)
		if err != nil {
			return nil, err
		}

		for key, value := range obj {
			text, ok := value.(string)
			_, twice := env[key]

			switch {
			case !envNamePattern.MatchString(key):
				return nil, &SecretFetchError{ARN: arn, Key: key, err: errSecretKeyName}
			case !ok:
				return nil, &SecretFetchError{ARN: arn, Key: key, err: errSecretKeyNotString}
			case twice:
				return nil, &SecretFetchError{ARN: arn, Key: key, err: errSecretKeyTwice}
			}

			env[key] = text
		}
	}

	return env, nil
}

func secretObject(ctx context.Context, fetcher SecretFetcher, arn string) (map[string]any, error) {
	body, err := fetcher.SecretString(ctx, arn)
	if err != nil {
		return nil, fmt.Errorf("read secret string: %w", err)
	}

	var obj map[string]any
	if err := json.Unmarshal([]byte(body), &obj); err != nil || obj == nil {
		return nil, &SecretFetchError{ARN: arn, err: errSecretNotJSON}
	}

	return obj, nil
}
