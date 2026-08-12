package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
)

var errInvalidAWSRef = errors.New("aws object requires arn and key")

func mergeJSON(dst, src any) any {
	dstMap, dstOK := dst.(map[string]any)

	srcMap, srcOK := src.(map[string]any)
	if !dstOK || !srcOK {
		return src
	}

	out := maps.Clone(dstMap)
	for key, value := range srcMap {
		if existing, ok := out[key]; ok {
			out[key] = mergeJSON(existing, value)
			continue
		}

		out[key] = value
	}

	return out
}

func resolveAWS(value any, fetcher SecretFetcher) (any, error) {
	switch node := value.(type) {
	case map[string]any:
		arn, key, isRef, err := awsRef(node)
		if err != nil {
			return nil, err
		}

		if isRef {
			return secretField(fetcher, arn, key)
		}

		out := make(map[string]any, len(node))
		for key, child := range node {
			resolved, err := resolveAWS(child, fetcher)
			if err != nil {
				return nil, err
			}

			out[key] = resolved
		}

		return out, nil
	case []any:
		out := make([]any, len(node))
		for i, child := range node {
			resolved, err := resolveAWS(child, fetcher)
			if err != nil {
				return nil, err
			}

			out[i] = resolved
		}

		return out, nil
	default:
		return value, nil
	}
}

func awsRef(node map[string]any) (arn, key string, ok bool, err error) {
	if len(node) != 1 {
		return "", "", false, nil
	}

	raw, exists := node["aws"]
	if !exists {
		return "", "", false, nil
	}

	fields, isMap := raw.(map[string]any)
	if !isMap {
		return "", "", false, &SecretFetchError{err: errInvalidAWSRef}
	}

	arn, _ = fields["arn"].(string)

	key, _ = fields["key"].(string)
	if arn == "" || key == "" {
		return "", "", false, &SecretFetchError{ARN: arn, Key: key, err: errInvalidAWSRef}
	}

	return arn, key, true, nil
}

func decodeObject(data []byte, what string) (map[string]any, error) {
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse %s JSON: %w", what, err)
	}

	obj, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("parse %s JSON: must be an object", what)
	}

	return obj, nil
}
