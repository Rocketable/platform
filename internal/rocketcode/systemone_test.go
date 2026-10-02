package rocketcode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
)

const (
	systemOneTestAPIKey = "sk-test-secret-key"
	systemOneJevBody    = `{"model":"jev-1.13.0","answers":{"department":{"type":"choice","choice":"billing","confidence":1.0,"probabilities":{"billing":1.0,"sales":0.0,"technical":0.0}},"urgency":{"type":"score","score":1.01,"confidence":0.97,"legend":{"0":"Can wait for normal support","1":"Needs prompt attention","2":"Actively blocking revenue"},"probabilities":{"0":0.0,"1":0.98,"2":0.02}},"refund":{"type":"noul","noul":0.99}},"usage":{"input_tokens":422,"output_tokens":69}}`
	systemOneDeptOKBody = `{"model":"jev-1.13.0","answers":{"department":{"type":"choice","choice":"billing","confidence":1.0}},"usage":{"input_tokens":1,"output_tokens":1}}`
)

func TestSystemOneHappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/systemone", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, systemOneJevBody)
	}))
	t.Cleanup(server.Close)

	got, err := callSystemOne(context.Background(), systemOneTestConfig(server.URL), newSystemOneHTTPClient(), "ticket: refund", systemOneFullQuestions())
	require.NoError(t, err)

	dict := systemOneRequireDict(t, got)
	require.Equal(t, starlark.None, systemOneDictGet(t, dict, "error"))

	var parsed systemOneJevResult
	require.NoError(t, json.Unmarshal(starlarkJSON(t, got), &parsed))
	require.Equal(t, "jev-1.13.0", parsed.Model)
	require.Equal(t, "choice", parsed.Answers.Department.Type)
	require.Equal(t, "billing", parsed.Answers.Department.Choice)
	require.InDelta(t, 1.0, parsed.Answers.Department.Confidence, 1e-9)
	require.Equal(t, map[string]float64{"billing": 1.0, "sales": 0.0, "technical": 0.0}, parsed.Answers.Department.Probabilities)
	require.Equal(t, "score", parsed.Answers.Urgency.Type)
	require.InDelta(t, 1.01, parsed.Answers.Urgency.Score, 1e-9)
	require.InDelta(t, 0.97, parsed.Answers.Urgency.Confidence, 1e-9)
	require.Equal(t, map[string]string{"0": "Can wait for normal support", "1": "Needs prompt attention", "2": "Actively blocking revenue"}, parsed.Answers.Urgency.Legend)
	require.Equal(t, "noul", parsed.Answers.Refund.Type)
	require.InDelta(t, 0.99, parsed.Answers.Refund.Noul, 1e-9)
	require.Equal(t, 422, parsed.Usage.InputTokens)
	require.Equal(t, 69, parsed.Usage.OutputTokens)
	require.Nil(t, parsed.Error)
}

func TestSystemOneRequestShape(t *testing.T) {
	state := map[string]any{"ticket": "refund requested", "tags": []any{"billing", "vip"}}
	questions := systemOneFullQuestions()

	var captured struct {
		Method string
		Path   string
		Auth   string
		Type   string
		Body   systemOneRequest
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.Method = r.Method
		captured.Path = r.URL.Path
		captured.Auth = r.Header.Get("Authorization")
		captured.Type = r.Header.Get("Content-Type")
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&captured.Body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, systemOneJevBody)
	}))
	t.Cleanup(server.Close)

	cfg := SystemOne{APIKey: systemOneTestAPIKey, Model: "jev-1.13.0", BaseURL: server.URL}
	_, err := callSystemOne(context.Background(), cfg, newSystemOneHTTPClient(), state, questions)
	require.NoError(t, err)

	require.Equal(t, http.MethodPost, captured.Method)
	require.Equal(t, "/systemone", captured.Path)
	require.Equal(t, "Bearer "+systemOneTestAPIKey, captured.Auth)
	require.Equal(t, "application/json", captured.Type)
	require.Equal(t, "jev-1.13.0", captured.Body.Model)
	require.Equal(t, state, captured.Body.State)

	wantQuestions, err := json.Marshal(questions)
	require.NoError(t, err)
	gotQuestions, err := json.Marshal(captured.Body.Questions)
	require.NoError(t, err)
	require.JSONEq(t, string(wantQuestions), string(gotQuestions))
}

func TestSystemOneRetriesThenSucceeds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var attempts attemptCounter

		client := systemOnePipeClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if attempts.add() == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, systemOneDeptOKBody)
		}))

		got, err := callSystemOne(context.Background(), systemOneTestConfig("http://systemone.test"), client, "state", systemOneDeptQuestions())
		require.NoError(t, err)
		dict := systemOneRequireDict(t, got)
		require.Equal(t, starlark.None, systemOneDictGet(t, dict, "error"))
		require.Equal(t, 2, attempts.get())
	})
}

func TestSystemOneRetryAfterExceedsDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var attempts attemptCounter

		client := systemOnePipeClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts.add()
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
		}))

		start := time.Now()
		got, err := callSystemOne(context.Background(), systemOneTestConfig("http://systemone.test"), client, "state", systemOneDeptQuestions())
		require.NoError(t, err)
		require.Equal(t, start, time.Now())
		kind, status, _ := systemOneErrorFields(t, got)
		require.Equal(t, "rate_limited", kind)
		require.Equal(t, http.StatusTooManyRequests, status)
		require.Equal(t, 1, attempts.get())
	})
}

func TestSystemOneStatusKinds(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		wantKind     string
		wantAttempts int
	}{
		{"rate limited", http.StatusTooManyRequests, `{"error":"slow down"}`, "rate_limited", 3},
		{"overloaded 529", 529, "", "overloaded", 3},
		{"overloaded 503", http.StatusServiceUnavailable, "", "overloaded", 3},
		{"unavailable 500", http.StatusInternalServerError, "", "unavailable", 3},
		{"unavailable 408", http.StatusRequestTimeout, "", "unavailable", 3},
		{"unauthorized 401", http.StatusUnauthorized, "", "unauthorized", 1},
		{"unauthorized 403", http.StatusForbidden, "", "unauthorized", 1},
		{"too large 413", http.StatusRequestEntityTooLarge, "payload too large", "too_large", 1},
		{"too large 422", http.StatusUnprocessableEntity, `{"error":"request exceeds size limit"}`, "too_large", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var attempts attemptCounter

				client := systemOnePipeClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					attempts.add()
					w.WriteHeader(tt.status)
					_, _ = io.WriteString(w, tt.body)
				}))

				got, err := callSystemOne(context.Background(), systemOneTestConfig("http://systemone.test"), client, "state", systemOneDeptQuestions())
				require.NoError(t, err)
				kind, status, message := systemOneErrorFields(t, got)
				require.Equal(t, tt.wantKind, kind)
				require.Equal(t, tt.status, status)
				require.Contains(t, message, strconv.Itoa(tt.status))
				require.Contains(t, message, tt.body)
				require.Equal(t, tt.wantAttempts, attempts.get())
			})
		})
	}
}

func TestSystemOneConnectionRefused(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var attempts attemptCounter

		client := newSystemOneHTTPClient()
		client.Transport = &http.Transport{
			DialContext: func(context.Context, string, string) (net.Conn, error) {
				attempts.add()
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
			},
		}

		got, err := callSystemOne(context.Background(), systemOneTestConfig("http://127.0.0.1:1"), client, "state", systemOneDeptQuestions())
		require.NoError(t, err)
		kind, status, message := systemOneErrorFields(t, got)
		require.Equal(t, "unavailable", kind)
		require.Equal(t, 0, status)
		require.NotContains(t, message, "connection refused")
		require.NotContains(t, message, "dial")
		require.Equal(t, 3, attempts.get())
	})
}

func TestSystemOneRejected(t *testing.T) {
	body := `{"error":"` + strings.Repeat("invalid score levels ", 40) + `"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)

	got, err := callSystemOne(context.Background(), systemOneTestConfig(server.URL), newSystemOneHTTPClient(), "state", systemOneDeptQuestions())
	require.NoError(t, err)
	kind, status, message := systemOneErrorFields(t, got)
	require.Equal(t, "rejected", kind)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Contains(t, message, "422")
	require.Contains(t, message, "invalid score levels")
	require.LessOrEqual(t, len(message), len("422 ")+500)
	require.NotContains(t, message, body[len(body)-20:])
}

func TestSystemOneTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := newSystemOneHTTPClient()
		client.Transport = &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(context.Context, string, string) (net.Conn, error) {
				clientConn, serverConn := net.Pipe()

				go func() {
					defer func() { _ = serverConn.Close() }()

					_, _ = io.Copy(io.Discard, serverConn)
				}()

				return clientConn, nil
			},
		}

		got, err := callSystemOne(context.Background(), systemOneTestConfig("http://systemone.test"), client, "state", systemOneDeptQuestions())
		require.NoError(t, err)
		kind, status, message := systemOneErrorFields(t, got)
		require.Equal(t, "timeout", kind)
		require.Equal(t, 0, status)
		require.NotContains(t, message, "deadline")
		require.NotContains(t, message, "context")
	})
}

func TestSystemOneParentCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := callSystemOne(ctx, systemOneTestConfig("http://127.0.0.1:1"), newSystemOneHTTPClient(), "state", systemOneDeptQuestions())
	require.Nil(t, got)
	require.ErrorIs(t, err, context.Canceled)
}

func TestSystemOneInvalidResponse(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{"not json", "not-json"},
		{"missing question id", `{"model":"jev-1.13.0","answers":{"other":{"type":"noul","noul":0.1}}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tt.body)
			}))
			t.Cleanup(server.Close)

			got, err := callSystemOne(context.Background(), systemOneTestConfig(server.URL), newSystemOneHTTPClient(), "state", systemOneDeptQuestions())
			require.NoError(t, err)
			kind, status, _ := systemOneErrorFields(t, got)
			require.Equal(t, "invalid_response", kind)
			require.Equal(t, http.StatusOK, status)
		})
	}
}

func TestSystemOneUnknownAnswerType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"jev-1.13.0","answers":{"weird":{"type":"newthing","foo":7}},"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(server.Close)

	questions := map[string]any{"weird": map[string]any{"type": "newthing", "instructions": "pass through"}}
	got, err := callSystemOne(context.Background(), systemOneTestConfig(server.URL), newSystemOneHTTPClient(), "state", questions)
	require.NoError(t, err)
	dict := systemOneRequireDict(t, got)
	require.Equal(t, starlark.None, systemOneDictGet(t, dict, "error"))
	answers := systemOneRequireDict(t, systemOneDictGet(t, dict, "answers"))
	weird := systemOneRequireDict(t, systemOneDictGet(t, answers, "weird"))
	require.Equal(t, starlark.String("newthing"), systemOneDictGet(t, weird, "type"))
	foo, err := starlark.AsInt32(systemOneDictGet(t, weird, "foo"))
	require.NoError(t, err)
	require.Equal(t, 7, foo)
}

func TestSystemOneRedactsAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "echo "+r.Header.Get("Authorization")+" "+systemOneTestAPIKey)
	}))
	t.Cleanup(server.Close)

	got, err := callSystemOne(context.Background(), systemOneTestConfig(server.URL), newSystemOneHTTPClient(), "state", systemOneDeptQuestions())
	require.NoError(t, err)
	_, _, message := systemOneErrorFields(t, got)
	require.NotContains(t, message, systemOneTestAPIKey)
}

func TestSystemOneDoesNotFollowRedirect(t *testing.T) {
	var targetHits attemptCounter

	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetHits.add()
	}))
	t.Cleanup(target.Close)

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/stolen", http.StatusFound)
	}))
	t.Cleanup(source.Close)

	got, err := callSystemOne(context.Background(), systemOneTestConfig(source.URL), newSystemOneHTTPClient(), "state", systemOneDeptQuestions())
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, 0, targetHits.get())
}

type systemOneJevResult struct {
	Model   string `json:"model"`
	Answers struct {
		Department struct {
			Type          string             `json:"type"`
			Choice        string             `json:"choice"`
			Confidence    float64            `json:"confidence"`
			Probabilities map[string]float64 `json:"probabilities"`
		} `json:"department"`
		Urgency struct {
			Type          string             `json:"type"`
			Score         float64            `json:"score"`
			Confidence    float64            `json:"confidence"`
			Legend        map[string]string  `json:"legend"`
			Probabilities map[string]float64 `json:"probabilities"`
		} `json:"urgency"`
		Refund struct {
			Type string  `json:"type"`
			Noul float64 `json:"noul"`
		} `json:"refund"`
	} `json:"answers"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct{} `json:"error"`
}

type attemptCounter struct {
	mu sync.Mutex
	n  int
}

func (c *attemptCounter) add() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.n++

	return c.n
}

func (c *attemptCounter) get() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.n
}

type systemOnePipeListener struct {
	conn net.Conn
	done chan struct{}
	once sync.Once
	ch   chan net.Conn
}

func systemOnePipeClient(t *testing.T, handler http.Handler) *http.Client {
	t.Helper()

	client := newSystemOneHTTPClient()
	client.Transport = &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			clientConn, serverConn := net.Pipe()

			ln := &systemOnePipeListener{conn: serverConn, done: make(chan struct{}), ch: make(chan net.Conn, 1)}
			ln.ch <- serverConn

			srv := &http.Server{Handler: handler}
			go func() { _ = srv.Serve(ln) }()

			t.Cleanup(func() { _ = srv.Close() })

			return clientConn, nil
		},
	}

	return client
}

func (l *systemOnePipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.ch:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *systemOnePipeListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *systemOnePipeListener) Addr() net.Addr {
	return l.conn.LocalAddr()
}

func systemOneTestConfig(baseURL string) SystemOne {
	return SystemOne{APIKey: systemOneTestAPIKey, Model: "jev-1.13.0", BaseURL: baseURL}
}

func systemOneFullQuestions() map[string]any {
	return map[string]any{
		"department": map[string]any{
			"type":         "choice",
			"instructions": "Which department should handle this?",
			"criteria":     []any{"billing", "sales", "technical"},
		},
		"urgency": map[string]any{
			"type":         "score",
			"instructions": "How urgent is this?",
			"criteria": map[string]any{
				"0": "Can wait for normal support",
				"1": "Needs prompt attention",
				"2": "Actively blocking revenue",
			},
		},
		"refund": map[string]any{
			"type":         "noul",
			"instructions": "Should we issue a refund?",
		},
	}
}

func systemOneDeptQuestions() map[string]any {
	return map[string]any{
		"department": map[string]any{
			"type":         "choice",
			"instructions": "Which department?",
			"criteria":     []any{"billing", "sales", "technical"},
		},
	}
}

func systemOneRequireDict(t *testing.T, v starlark.Value) *starlark.Dict {
	t.Helper()

	dict, ok := v.(*starlark.Dict)
	require.True(t, ok, "got %T", v)

	return dict
}

func systemOneDictGet(t *testing.T, dict *starlark.Dict, key string) starlark.Value {
	t.Helper()

	got, found, err := dict.Get(starlark.String(key))
	require.NoError(t, err)
	require.True(t, found, "missing %s", key)

	return got
}

func systemOneErrorFields(t *testing.T, v starlark.Value) (kind string, status int, message string) {
	t.Helper()
	errDict := systemOneRequireDict(t, systemOneDictGet(t, systemOneRequireDict(t, v), "error"))
	kind, _ = starlark.AsString(systemOneDictGet(t, errDict, "kind"))
	status, err := starlark.AsInt32(systemOneDictGet(t, errDict, "status"))
	require.NoError(t, err)
	message, _ = starlark.AsString(systemOneDictGet(t, errDict, "message"))

	return kind, status, message
}

func starlarkJSON(t *testing.T, v starlark.Value) []byte {
	t.Helper()

	encoded, err := starlark.Call(&starlark.Thread{Name: "systemone-test"}, starjson.Module.Members["encode"], starlark.Tuple{v}, nil)
	require.NoError(t, err)

	s, ok := starlark.AsString(encoded)
	require.True(t, ok)

	return []byte(s)
}

const systemOneNeedsHumanBody = `{"model":"jev-1.13.0","answers":{"needs_human":{"type":"noul","noul":0.91}},"usage":{"input_tokens":1,"output_tokens":1}}`

func TestSystemOneAbsentWithoutConfig(t *testing.T) {
	var perms PermissionSet
	require.NoError(t, perms.Allow("systemone", "*"))
	loop := systemOneRuntime(t, SystemOne{}, perms)
	assert.NotContains(t, loop.CodeModeHosts, "systemone")
	assert.NotContains(t, loop.Tools, "systemone")
}

func TestSystemOneAbsentWithoutGrant(t *testing.T) {
	var hits attemptCounter

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.add()
	}))
	t.Cleanup(server.Close)

	var perms PermissionSet
	require.NoError(t, perms.Allow("read", "*"))
	loop := systemOneRuntime(t, systemOneTestConfig(server.URL), perms)
	assert.NotContains(t, loop.CodeModeHosts, "systemone")
	require.Contains(t, loop.Tools, executeToolName)

	_, err := systemOneCallExecute(t, loop, `def main():
    return systemone(state="x", questions={"needs_human": {"type": "noul", "instructions": "Need a human?"}})
`)
	require.Error(t, err)
	assert.Equal(t, 0, hits.get())
}

func TestSystemOneExecuteOnlyWhenGranted(t *testing.T) {
	var perms PermissionSet
	require.NoError(t, perms.Allow("systemone", "*"))
	loop := systemOneRuntime(t, systemOneTestConfig("http://127.0.0.1:1"), perms)
	assert.Contains(t, loop.CodeModeHosts, "systemone")
	assert.NotContains(t, loop.Tools, "systemone")
	require.Contains(t, loop.Tools, executeToolName)
}

func TestSystemOneTaskChildGrant(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })

	sfs := &sandboxedFileSystem{mu: sync.Mutex{}, root: root}
	base := makeSandboxedTools(sfs, nil)
	base["systemone"] = systemOneTool(systemOneTestConfig("http://127.0.0.1:1"), newSystemOneHTTPClient())
	factory := &toolFactory{baseTools: base}

	var granted PermissionSet
	require.NoError(t, granted.Allow("systemone", "*"))
	_, hosts := factory.assembleTools(&Agent{Name: "child", Permission: granted})
	assert.Contains(t, hosts, "systemone")

	_, hosts = factory.assembleTools(&Agent{Name: "child", Permission: PermissionSet{}})
	assert.NotContains(t, hosts, "systemone")
}

func TestSystemOneExecuteMapPartialError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body systemOneRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		if body.State == "7" {
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, systemOneNeedsHumanBody)
	}))
	t.Cleanup(server.Close)

	var perms PermissionSet
	require.NoError(t, perms.Allow("systemone", "*"))
	loop := systemOneRuntime(t, systemOneTestConfig(server.URL), perms)

	result, err := systemOneCallExecute(t, loop, `def main():
    results = map([0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19], lambda n: systemone(state=str(n), questions={"needs_human": {"type": "noul", "instructions": "Need a human?"}}), concurrency=4)
    answers = 0
    errors = 0
    for r in results:
        if r["error"]:
            errors += 1
        else:
            answers += 1
    return str(answers) + " " + str(errors)
`)
	require.NoError(t, err)
	assert.Equal(t, "19 1", result.Output)
}

func TestSystemOneMalformedArgumentsRaise(t *testing.T) {
	var hits attemptCounter

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.add()
	}))
	t.Cleanup(server.Close)

	var perms PermissionSet
	require.NoError(t, perms.Allow("systemone", "*"))
	loop := systemOneRuntime(t, systemOneTestConfig(server.URL), perms)

	options := make([]string, 300)
	for i := range options {
		options[i] = `"o` + strconv.Itoa(i) + `": "x"`
	}

	tests := []struct {
		name    string
		call    string
		wantErr string
	}{
		{"choice with 300 options", `systemone(state="x", questions={"too_many": {"type": "choice", "instructions": "pick", "criteria": {` + strings.Join(options, ", ") + `}}})`, "too_many"},
		{"score with one level", `systemone(state="x", questions={"urgency": {"type": "score", "instructions": "rate", "criteria": ["only"]}})`, "urgency"},
		{"unknown type", `systemone(state="x", questions={"weird": {"type": "rank", "instructions": "x"}})`, "weird"},
		{"missing questions", `systemone(state="x")`, "questions"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := systemOneCallExecute(t, loop, "def main():\n    return "+tt.call+"\n")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Equal(t, 0, hits.get())
		})
	}
}

func TestSystemOneCatalogLine(t *testing.T) {
	line := strings.TrimSpace(strings.SplitN(systemOneDescription, "\n", 2)[0])
	assert.LessOrEqual(t, len(line), 120)
	assert.Contains(t, strings.ToLower(line), "task")
	assert.Contains(t, line, `search(query="systemone")`)
	assert.Contains(t, systemOneDescription, systemOneExampleScript)
}

func TestSystemOneExampleScript(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, systemOneNeedsHumanBody)
	}))
	t.Cleanup(server.Close)

	var perms PermissionSet
	require.NoError(t, perms.Allow("systemone", "*"))
	loop := systemOneRuntime(t, systemOneTestConfig(server.URL), perms)

	result, err := systemOneCallExecute(t, loop, systemOneExampleScript)
	require.NoError(t, err)
	assert.Equal(t, "escalate", result.Output)
	assert.Contains(t, systemOneExampleScript, `["noul"]`)
	assert.NotContains(t, systemOneExampleScript, "confidence")
}

func systemOneRuntime(t *testing.T, sys SystemOne, perms PermissionSet) *Runtime {
	t.Helper()

	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })

	cfg := testConfig(dir)
	cfg.SystemOne = sys
	client := openai.NewClient()
	loop, err := NewWithModelResolver(testModelResolverFunc(func(model string) (*openai.Client, ProviderOrigin, error) {
		return &client, ProviderOrigin{Provider: "openai", Model: model}, nil
	}), cfg, root, Agents{Items: map[string]Agent{
		"main": {Name: "main", Model: "gpt-5.5", Prompt: "prompt", Permission: perms},
	}}, Skills{Items: map[string]Skill{}}, "main", nil)
	require.NoError(t, err)

	return loop
}

func systemOneCallExecute(t *testing.T, loop *Runtime, code string) (ToolResult, error) {
	t.Helper()

	raw, err := json.Marshal(struct {
		Code string `json:"code"`
	}{code})
	require.NoError(t, err)
	ctx := withToolCallContext(t.Context(), loop, nil, "")

	return loop.Tools[executeToolName].Call(ctx, raw, nil, emptyToolCallMetadata())
}
