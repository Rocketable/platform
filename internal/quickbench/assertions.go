package quickbench

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
)

func evaluateAssertions(expected expected, text string, calls []observedToolCall) []string {
	failures := []string{}
	for _, assertion := range expected.Text {
		match, err := regexp.MatchString(assertion.Regexp, text)
		if err != nil {
			failures = append(failures, fmt.Sprintf("regexp %q failed to compile: %v", assertion.Regexp, err))
			continue
		}

		if !match {
			failures = append(failures, fmt.Sprintf("text did not match regexp %q", assertion.Regexp))
		}
	}

	if len(expected.Tools.Calls) == 0 {
		return failures
	}

	if expected.Tools.Ordered {
		next := 0
		for _, wanted := range expected.Tools.Calls {
			found := false
			for next < len(calls) {
				if toolCallMatches(wanted, calls[next]) {
					found = true
					next++
					break
				}

				next++
			}

			if !found {
				failures = append(failures, fmt.Sprintf("tool %q was not called in the expected order with matching arguments", wanted.Name))
			}
		}

		return failures
	}

	for _, wanted := range expected.Tools.Calls {
		found := false
		for _, observed := range calls {
			if toolCallMatches(wanted, observed) {
				found = true
				break
			}
		}

		if !found {
			failures = append(failures, fmt.Sprintf("tool %q was not called with matching arguments", wanted.Name))
		}
	}

	return failures
}

func toolCallMatches(expected toolCallExpected, observed observedToolCall) bool {
	if expected.Name != observed.Name {
		return false
	}

	return valueContains(observed.Arguments, expected.Arguments)
}

func valueContains(actual, expected any) bool {
	actual = normalizeJSONValue(actual)
	expected = normalizeJSONValue(expected)

	expectedMap, ok := expected.(map[string]any)
	if !ok {
		return reflect.DeepEqual(actual, expected)
	}

	actualMap, ok := actual.(map[string]any)
	if !ok {
		return false
	}

	for key, expectedValue := range expectedMap {
		actualValue, ok := actualMap[key]
		if !ok {
			return false
		}

		if !valueContains(actualValue, expectedValue) {
			return false
		}
	}

	return true
}

func normalizeJSONValue(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}

	var normalized any
	if err := json.Unmarshal(data, &normalized); err != nil {
		return value
	}

	return normalized
}
