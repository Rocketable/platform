package rocketcode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
)

const systemOneCallTimeout = 30 * time.Second

// SystemOne is the operator-configured TypeSafe System One endpoint.
type SystemOne struct {
	APIKey  string
	Model   string
	BaseURL string
}

type systemOneErrorKind string

const (
	systemOneErrorTimeout         systemOneErrorKind = "timeout"
	systemOneErrorRateLimited     systemOneErrorKind = "rate_limited"
	systemOneErrorOverloaded      systemOneErrorKind = "overloaded"
	systemOneErrorUnauthorized    systemOneErrorKind = "unauthorized"
	systemOneErrorTooLarge        systemOneErrorKind = "too_large"
	systemOneErrorRejected        systemOneErrorKind = "rejected"
	systemOneErrorUnavailable     systemOneErrorKind = "unavailable"
	systemOneErrorInvalidResponse systemOneErrorKind = "invalid_response"
)

type systemOneRequest struct {
	Model     string         `json:"model"`
	State     any            `json:"state"`
	Questions map[string]any `json:"questions"`
}

func newSystemOneHTTPClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func callSystemOne(ctx context.Context, cfg SystemOne, client *http.Client, state any, questions map[string]any) (starlark.Value, error) {
	payload, err := json.Marshal(systemOneRequest{Model: cfg.Model, State: state, Questions: questions})
	if err != nil {
		return nil, fmt.Errorf("encode systemone request: %w", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, systemOneCallTimeout)
	defer cancel()

	endpoint := strings.TrimRight(cfg.BaseURL, "/") + "/systemone"

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("build systemone request: %w", err)
		}

		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		req.Header.Set("Content-Type", "application/json")

		resp, errDo := client.Do(req)

		var body []byte
		if errDo == nil {
			body, errDo = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}

		backoff := 500 * time.Millisecond << attempt
		kind, status, wait := systemOneErrorUnavailable, 0, backoff-time.Duration(rand.Int64N(int64(backoff/4)+1))

		if errDo == nil {
			status = resp.StatusCode
			if status >= 200 && status < 300 {
				if dict, ok := systemOneSuccess(body, questions); ok {
					return dict, nil
				}

				return systemOneFail(systemOneErrorInvalidResponse, status, cfg.APIKey, body), nil
			}

			kind = systemOneKind(status, body)
			if kind != systemOneErrorRateLimited && kind != systemOneErrorOverloaded && kind != systemOneErrorUnavailable {
				return systemOneFail(kind, status, cfg.APIKey, body), nil
			}

			if seconds, errParse := strconv.Atoi(resp.Header.Get("Retry-After")); errParse == nil && seconds >= 0 {
				wait = time.Duration(seconds) * time.Second
			}
		}

		if ctx.Err() != nil {
			return nil, fmt.Errorf("systemone call: %w", context.Cause(ctx))
		}

		if callCtx.Err() != nil {
			return systemOneFail(systemOneErrorTimeout, 0, cfg.APIKey, nil), nil
		}

		deadline, _ := callCtx.Deadline()
		if attempt == 2 || wait >= time.Until(deadline) {
			return systemOneFail(kind, status, cfg.APIKey, body), nil
		}

		select {
		case <-callCtx.Done():
		case <-time.After(wait):
		}
	}
}

func systemOneSuccess(body []byte, questions map[string]any) (*starlark.Dict, bool) {
	decoded, err := starlark.Call(&starlark.Thread{Name: "systemone"}, starjson.Module.Members["decode"], starlark.Tuple{starlark.String(string(body))}, nil)
	if err != nil {
		return nil, false
	}

	dict, ok := decoded.(*starlark.Dict)
	if !ok {
		return nil, false
	}

	// String keys are always hashable, so Get cannot fail here.
	answersVal, _, _ := dict.Get(starlark.String("answers"))

	answers, ok := answersVal.(*starlark.Dict)
	if !ok {
		return nil, false
	}

	for id := range questions {
		if _, found, _ := answers.Get(starlark.String(id)); !found {
			return nil, false
		}
	}

	_ = dict.SetKey(starlark.String("error"), starlark.None)

	return dict, true
}

func systemOneFail(kind systemOneErrorKind, status int, apiKey string, body []byte) *starlark.Dict {
	inner := starlark.NewDict(3)
	_ = inner.SetKey(starlark.String("kind"), starlark.String(kind))
	_ = inner.SetKey(starlark.String("status"), starlark.MakeInt(status))

	message := strconv.Itoa(status)
	if redacted := strings.ReplaceAll(string(body), apiKey, "[redacted]"); redacted != "" {
		message += " " + redacted[:min(len(redacted), 500)]
	}

	_ = inner.SetKey(starlark.String("message"), starlark.String(message))
	outer := starlark.NewDict(1)
	_ = outer.SetKey(starlark.String("error"), inner)

	return outer
}

func systemOneKind(status int, body []byte) systemOneErrorKind {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return systemOneErrorUnauthorized
	case status == http.StatusTooManyRequests:
		return systemOneErrorRateLimited
	case status == 529 || status == http.StatusServiceUnavailable:
		return systemOneErrorOverloaded
	case status == http.StatusRequestTimeout || status >= 500:
		return systemOneErrorUnavailable
	case status == http.StatusRequestEntityTooLarge || (status == http.StatusUnprocessableEntity && strings.Contains(strings.ToLower(string(body)), "size")):
		return systemOneErrorTooLarge
	default:
		return systemOneErrorRejected
	}
}

const systemOneExampleScript = `def main():
    r = systemone(state="ticket: refund on order 123", questions={"needs_human": {"type": "noul", "instructions": "Does this operation need a human?"}})
    if r["error"]:
        return "error"
    return "escalate" if r["answers"]["needs_human"]["noul"] >= 0.5 else "auto"
`

const systemOneDescription = `Quick yes/no, choice, or score classification. Replaces task; search(query="systemone") for usage.

Use this instead of delegating a classification to a subagent with task. Write narrow questions. Offer an abstain option such as a "none" choice when one is needed. Keep arithmetic and date comparisons in code. Check Choice/Score confidence before acting. Noul answers have no confidence field; threshold on the noul probability (values near 0.5 mean unsure). Build state from files with read(filePath=..., plain=True), which omits line numbers. State is token-limited, so trim it generously with string slicing. Fan-outs should pass a small concurrency= (for example 4) to map/gather and handle rate_limited values. Failed calls return r["error"] dicts with kind/status/message. Malformed questions raise.

Example:
` + systemOneExampleScript

func addSystemOneTool(baseTools map[string]looperTool, cfg SystemOne) {
	if cfg == (SystemOne{}) {
		return
	}

	baseTools["systemone"] = systemOneTool(cfg, newSystemOneHTTPClient())
}

func systemOneTool(cfg SystemOne, client *http.Client) looperTool {
	return looperTool{
		Definition: *functionTool("systemone", systemOneDescription, map[string]any{
			"state":     map[string]any{"type": []string{"string", "object", "array"}},
			"questions": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "object"}},
		}),
		Permission: "systemone",
		Call: func(ctx context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
			var params systemOneRequest
			if err := decodeToolParams(raw, &params); err != nil {
				return ToolResult{}, err
			}

			for id, rawQuestion := range params.Questions {
				question := rawQuestion.(map[string]any)

				kind, _ := question["type"].(string)
				switch kind {
				case "noul":
				case "choice":
					if n := systemOneCriteriaLen(question["criteria"]); n > 255 {
						return ToolResult{}, fmt.Errorf("systemone question %q has %d choice options (max 255)", id, n)
					}
				case "score":
					if n := systemOneCriteriaLen(question["criteria"]); n < 2 || n > 10 {
						return ToolResult{}, fmt.Errorf("systemone question %q has %d score levels (need 2-10)", id, n)
					}
				default:
					return ToolResult{}, fmt.Errorf("systemone question %q has unknown type %q", id, kind)
				}
			}

			value, err := callSystemOne(ctx, cfg, client, params.State, params.Questions)
			if err != nil {
				return ToolResult{}, err
			}

			encoded, err := starlark.Call(&starlark.Thread{Name: "systemone"}, starjson.Module.Members["encode"], starlark.Tuple{value}, nil)
			if err != nil {
				return ToolResult{}, fmt.Errorf("encode systemone result: %w", err)
			}

			return ToolResult{Output: string(encoded.(starlark.String)), Data: value}, nil
		},
	}
}

func systemOneCriteriaLen(criteria any) int {
	if options, ok := criteria.(map[string]any); ok {
		return len(options)
	}

	levels, _ := criteria.([]any)

	return len(levels)
}
