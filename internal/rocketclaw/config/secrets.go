package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

var (
	errIncompleteARN      = errors.New("must be a complete secretsmanager ARN")
	errSecretKeyMissing   = errors.New("key not found")
	errSecretKeyNotString = errors.New("key is not a string")
	errSecretNotJSON      = errors.New("secret is not a JSON object")
	errSecretEmpty        = errors.New("secret string is empty")
)

// SecretFetcher returns a Secrets Manager SecretString.
type SecretFetcher interface {
	SecretString(arn string) (string, error)
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

// AWSFetcher loads SecretString with the default AWS credential chain.
type AWSFetcher struct{}

// SecretString fetches the current SecretString for arn.
func (AWSFetcher) SecretString(arn string) (string, error) {
	region, err := secretsManagerRegion(arn)
	if err != nil {
		return "", err
	}

	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(region))
	if err != nil {
		return "", &SecretFetchError{ARN: arn, err: err}
	}

	out, err := secretsmanager.NewFromConfig(cfg).GetSecretValue(context.Background(), &secretsmanager.GetSecretValueInput{
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

func secretField(fetcher SecretFetcher, arn, key string) (string, error) {
	body, err := fetcher.SecretString(arn)
	if err != nil {
		return "", fmt.Errorf("read secret string: %w", err)
	}

	var obj map[string]any
	if err := json.Unmarshal([]byte(body), &obj); err != nil || obj == nil {
		return "", &SecretFetchError{ARN: arn, Key: key, err: errSecretNotJSON}
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
