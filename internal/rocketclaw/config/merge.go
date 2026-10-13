package config

import (
	"encoding/json"
	"errors"
	"fmt"
)

var errInvalidAWSRef = errors.New("aws object requires arn and key")

// mergeJSON and resolveAWS consume the fresh decoded trees owned by Load.
func mergeJSON(dst, src any) any {
	dstMap, dstOK := dst.(map[string]any)

	srcMap, srcOK := src.(map[string]any)
	if !dstOK || !srcOK {
		return src
	}

	for key, value := range srcMap {
		dstMap[key] = mergeJSON(dstMap[key], value)
	}

	return dstMap
}

func resolveAWS(value any, fetcher SecretFetcher) (any, error) {
	switch node := value.(type) {
	case map[string]any:
		if raw, exists := node["aws"]; len(node) == 1 && exists {
			fields, isMap := raw.(map[string]any)
			if !isMap {
				return nil, &SecretFetchError{err: errInvalidAWSRef}
			}

			arn, _ := fields["arn"].(string)

			key, _ := fields["key"].(string)
			if arn == "" || key == "" {
				return nil, &SecretFetchError{ARN: arn, Key: key, err: errInvalidAWSRef}
			}

			return secretField(fetcher, arn, key)
		}

		for key, child := range node {
			resolved, err := resolveAWS(child, fetcher)
			if err != nil {
				return nil, err
			}

			node[key] = resolved
		}

		return node, nil
	case []any:
		for i, child := range node {
			resolved, err := resolveAWS(child, fetcher)
			if err != nil {
				return nil, err
			}

			node[i] = resolved
		}

		return node, nil
	default:
		return value, nil
	}
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
