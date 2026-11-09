package harness

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	otelsemconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

func testDecisionQuestions() []DecisionQuestion {
	return []DecisionQuestion{
		{Name: "safe", Type: DecisionPredicate, Instructions: "Is it safe?", Options: []DecisionOption{{Value: "true", Description: "Nothing could break"}, {Value: "false"}}},
		{Name: "outcome", Type: DecisionChoice, Instructions: "Allow or deny?", Options: []DecisionOption{{Value: "allow", Description: "Let it run"}, {Value: "deny"}}},
		{Name: "risk", Type: DecisionScore, Instructions: "How risky?", Options: []DecisionOption{{Value: "low"}, {Value: "medium"}, {Value: "high"}}},
	}
}

type recordedDecisionRequest struct {
	Path   string
	Header http.Header
	Body   string
}

// decisionServer answers every request with response and records each request.
func decisionServer(t *testing.T, status int, response string) (*httptest.Server, *[]recordedDecisionRequest) {
	t.Helper()

	var requests []recordedDecisionRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		requests = append(requests, recordedDecisionRequest{Path: r.URL.Path, Header: r.Header.Clone(), Body: string(data)})

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(server.Close)

	return server, &requests
}

func TestOpenAIDeciderAsksAndNormalizes(t *testing.T) {
	server, requests := decisionServer(t, http.StatusOK, `{"model":"gpt-6-luna","usage":{"input_tokens":120,"output_tokens":4},"answers":[
		{"type":"predicate","name":"safe","probability":0.98},
		{"type":"choice","name":"outcome","choice":"allow","confidence":0.93,"probabilities":[{"value":"allow","probability":0.95},{"value":"deny","probability":0.05}]},
		{"type":"score","name":"risk","score":0.3,"confidence":0.6,"probabilities":[{"value":0,"label":"low","probability":0.7},{"value":1,"label":"medium","probability":0.3},{"value":2,"label":"high","probability":0}]},
		{"type":"refusal","name":"extra"}]}`)
	client := openai.NewClient(option.WithAPIKey("key"), option.WithBaseURL(server.URL+"/v1/"))

	answers, usage, err := OpenAIDecider{Client: &client}.Decide(t.Context(), &DecisionRequest{Model: "gpt-6-luna", Instructions: "Policy.", Input: map[string]any{"message": "Evidence."}, Images: []DecisionImage{{MIME: "image/png", Base64: "iVBO"}}, Questions: testDecisionQuestions()})
	require.NoError(t, err)
	require.Equal(t, DecisionUsage{InputTokens: 120, OutputTokens: 4}, usage)
	require.Equal(t, []DecisionAnswer{
		{Name: "safe", Type: DecisionPredicate, Probability: 0.98, Confidence: 0.96},
		{Name: "outcome", Type: DecisionChoice, Choice: "allow", Confidence: 0.93, Probabilities: map[string]float64{"allow": 0.95, "deny": 0.05}},
		{Name: "risk", Type: DecisionScore, Choice: "low", Score: 0.3, Confidence: 0.6, Probabilities: map[string]float64{"low": 0.7, "medium": 0.3, "high": 0}},
		{Name: "extra", Refused: true},
	}, roundConfidence(answers))

	// A JSON state goes as its text, followed by the images; a predicate's meanings follow its instructions.
	require.Len(t, *requests, 1)
	require.Equal(t, "/v1/decisions", (*requests)[0].Path)
	require.JSONEq(t, `{
		"model": "gpt-6-luna",
		"input": [{"role": "developer", "content": "Policy."}, {"role": "user", "content": [{"type": "input_text", "text": "{\"message\":\"Evidence.\"}"}, {"type": "input_image", "image_url": "data:image/png;base64,iVBO"}]}],
		"questions": [
			{"type": "predicate", "name": "safe", "instructions": "Is it safe?\ntrue means: Nothing could break"},
			{"type": "choice", "name": "outcome", "instructions": "Allow or deny?", "choices": [{"value": "allow", "description": "Let it run"}, {"value": "deny"}]},
			{"type": "score", "name": "risk", "instructions": "How risky?", "levels": [{"label": "low"}, {"label": "medium"}, {"label": "high"}]}
		]
	}`, (*requests)[0].Body)
}

func TestTypeSafeDeciderAsksAndNormalizes(t *testing.T) {
	server, requests := decisionServer(t, http.StatusOK, `{"model":"jev-latest","usage":{"input_tokens":1,"output_tokens":1},"answers":{
		"safe":{"type":"noul","noul":0.98},
		"outcome":{"type":"choice","choice":"allow","confidence":0.93,"probabilities":{"allow":0.95,"deny":0.05}},
		"risk":{"type":"score","score":1.7,"confidence":0.55,"legend":{"0":"low","1":"medium","2":"high"},"probabilities":{"0":0.1,"1":0.1,"2":0.8}}}}`)

	answers, usage, err := TypeSafeDecider{HTTPClient: server.Client(), APIKey: "ts-key", BaseURL: server.URL + "/v1/"}.Decide(t.Context(), &DecisionRequest{Model: "jev-latest", Instructions: "Policy.", Input: map[string]any{"message": "Evidence."}, Questions: append(testDecisionQuestions(), DecisionQuestion{Name: "missing", Type: DecisionPredicate})})
	require.NoError(t, err)
	require.Equal(t, DecisionUsage{InputTokens: 1, OutputTokens: 1}, usage)
	require.Equal(t, []DecisionAnswer{
		{Name: "safe", Type: DecisionPredicate, Probability: 0.98, Confidence: 0.96},
		{Name: "outcome", Type: DecisionChoice, Choice: "allow", Confidence: 0.93, Probabilities: map[string]float64{"allow": 0.95, "deny": 0.05}},
		{Name: "risk", Type: DecisionScore, Choice: "high", Score: 1.7, Confidence: 0.55, Probabilities: map[string]float64{"low": 0.1, "medium": 0.1, "high": 0.8}},
		{Name: "missing", Refused: true},
	}, roundConfidence(answers))

	request := (*requests)[0]
	require.Equal(t, "/v1/systemone", request.Path)
	require.Equal(t, "Bearer ts-key", request.Header.Get("Authorization"))
	require.Equal(t, "true", request.Header.Get("Typesafe-Danger-Zone-Long-Context"))
	require.Empty(t, request.Header.Get("Typesafe-Danger-Zone-Images"))
	require.JSONEq(t, `{
		"model": "jev-latest",
		"state": {"message": "Evidence."},
		"questions": {
			"safe": {"type": "noul", "instructions": {"rules": "Policy.", "question": "Is it safe?"}, "criteria": {"true": "Nothing could break"}},
			"outcome": {"type": "choice", "instructions": {"rules": "Policy.", "question": "Allow or deny?"}, "criteria": {"allow": "Let it run", "deny": null}},
			"risk": {"type": "score", "instructions": {"rules": "Policy.", "question": "How risky?"}, "criteria": ["low", "medium", "high"]},
			"missing": {"type": "noul", "instructions": {"rules": "Policy.", "question": ""}}
		}
	}`, request.Body)
}

func TestTypeSafeDeciderReportsFailures(t *testing.T) {
	for _, tc := range []struct {
		name, response, wantErr string
		status                  int
	}{
		{name: "status", status: http.StatusUnprocessableEntity, response: `{"detail":[]}`, wantErr: "status 422"},
		{name: "unknown level", status: http.StatusOK, response: `{"answers":{"risk":{"type":"score","probabilities":{"7":1}}}}`, wantErr: `unknown score level "7"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := decisionServer(t, tc.status, tc.response)

			_, _, err := TypeSafeDecider{HTTPClient: server.Client(), APIKey: "k", BaseURL: server.URL}.Decide(t.Context(), &DecisionRequest{Model: "jev-latest", Input: "x", Questions: testDecisionQuestions()[2:]})
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestTypeSafeDeciderSendsImagesAsAssets(t *testing.T) {
	server, requests := decisionServer(t, http.StatusOK, `{"answers":{"safe":{"type":"noul","noul":0.9}}}`)
	decider := TypeSafeDecider{HTTPClient: server.Client(), APIKey: "k", BaseURL: server.URL}

	_, _, err := decider.Decide(t.Context(), &DecisionRequest{Model: "jev-latest", Input: map[string]any{"message": "x"}, Images: []DecisionImage{{MIME: "image/png", Base64: "iVBO"}, {MIME: "image/jpeg", Base64: "/9j/"}}, Questions: testDecisionQuestions()[:1]})
	require.NoError(t, err)

	var body struct {
		State  []json.RawMessage `json:"state"`
		Assets map[string]struct {
			Type       string `json:"type"`
			Format     string `json:"format"`
			DataBase64 string `json:"data_base64"`
		} `json:"assets"`
	}
	require.NoError(t, json.Unmarshal([]byte((*requests)[0].Body), &body))

	// The state lists each image's reference, then the input.
	var first, second string

	require.Len(t, body.State, 3)
	require.NoError(t, json.Unmarshal(body.State[0], &first))
	require.NoError(t, json.Unmarshal(body.State[1], &second))
	require.JSONEq(t, `{"message":"x"}`, string(body.State[2]))
	require.Len(t, body.Assets, 2)
	require.Equal(t, "png", body.Assets[first].Format)
	require.Equal(t, "iVBO", body.Assets[first].DataBase64)
	require.Equal(t, "jpg", body.Assets[second].Format)
	require.Equal(t, "image", body.Assets[second].Type)
	require.Equal(t, "true", (*requests)[0].Header.Get("Typesafe-Danger-Zone-Images"))

	_, _, err = decider.Decide(t.Context(), &DecisionRequest{Model: "jev-latest", Input: "x", Images: []DecisionImage{{MIME: "image/gif", Base64: "R0lG"}}, Questions: testDecisionQuestions()[:1]})
	require.EqualError(t, err, "typesafe decision: images must be PNG or JPEG, got image/gif")
}

func TestDecisionToolIsOptInAndCodeModeOnly(t *testing.T) {
	factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{}})
	factory.baseTools["decision"] = decisionTool(map[string]Decider{"openai": &mockDecider{}}, ObservabilityConfig{})

	for _, tc := range []struct {
		name       string
		permission string
		want       bool
	}{
		{name: "allowed models", permission: `decision: {"openai/*": allow}`, want: true},
		{name: "no rule", permission: "read: allow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := Agent{Name: "main", Permission: parsePermissionYAML(t, tc.permission)}
			model, hosts := factory.assembleTools(&agent)

			require.NotContains(t, model, "decision")

			tool, ok := hosts["decision"]
			require.Equal(t, tc.want, ok)

			if ok {
				require.True(t, strings.HasPrefix(tool.Definition.Description.Value, decisionToolSummary+" Models you may call: openai/*.\n"))
			}
		})
	}

	// Sudo turns on every tool, but only a configured decision provider adds this one.
	for _, deciders := range []map[string]Decider{nil, {"openai": &mockDecider{}}} {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, root.Close()) })

		client := openai.NewClient()
		config := testWorkspaceConfig(t, dir)
		config.Deciders = deciders
		loop, err := NewWithModelResolver(testResolverForClient(&client), config, root, Agents{Items: map[string]Agent{"main": {Name: "main", Model: "gpt-5.4", Permission: PermissionSet{Sudo: true}}}}, Skills{Items: map[string]Skill{}}, "main", nil)
		require.NoError(t, err)

		require.NotContains(t, loop.Tools, "decision")

		tool, ok := loop.CodeModeHosts["decision"]
		require.Equal(t, deciders != nil, ok)

		if ok {
			require.True(t, strings.HasPrefix(tool.Definition.Description.Value, decisionToolSummary+" Models you may call: openai/*.\n"))
		}
	}
}

const testDecisionToolArgs = `{"model":"openai/gpt-6-luna","state":{"message":"Please refund me."},"questions":{
	"tone":{"type":"choice","instructions":"What is the tone?","criteria":{"calm":"Neutral or polite","angry":"Upset"}},
	"urgency":{"type":"score","instructions":"How urgent is it?","criteria":["can wait","today"]},
	"billing":{"type":"bool","instructions":"Is it about billing?","criteria":{"true":"Money","false":"Anything else"}}}}`

func TestDecisionToolAnswersByQuestionName(t *testing.T) {
	answers := []DecisionAnswer{
		predicateAnswer("billing", 0.98),
		{Name: "tone", Type: DecisionChoice, Choice: "calm", Confidence: 0.7, Probabilities: map[string]float64{"calm": 0.8, "angry": 0.2}},
		{Name: "urgency", Type: DecisionScore, Choice: "today", Score: 0.9, Confidence: 0.6, Probabilities: map[string]float64{"can wait": 0.1, "today": 0.9}},
	}

	for _, tc := range []struct {
		name    string
		answers []DecisionAnswer
		err     error
		want    string
	}{
		{name: "answered", answers: answers, want: `{"model":"openai/gpt-6-luna","stop_reason":"stop","usage":{"input_tokens":2000,"output_tokens":0},"answers":{
			"billing":{"type":"bool","probability":0.98},
			"tone":{"type":"choice","choice":"calm","probabilities":{"calm":0.8,"angry":0.2},"confidence":0.7},
			"urgency":{"type":"score","score":0.9,"confidence":0.6}}}`},
		{name: "provider error", err: errors.New("provider down"), want: `{"model":"openai/gpt-6-luna","answers":{},"stop_reason":"error","error_message":"decide with openai/gpt-6-luna: provider down"}`},
		// The request was still billed.
		{name: "refusal", answers: answers[:2], want: `{"model":"openai/gpt-6-luna","answers":{},"usage":{"input_tokens":2000,"output_tokens":0},"stop_reason":"error","error_message":"the model did not answer \"urgency\""}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decider := &mockDecider{DecideFunc: func(ctx context.Context, _ *DecisionRequest) ([]DecisionAnswer, DecisionUsage, error) {
				// A hung provider cannot hold the script.
				deadline, ok := ctx.Deadline()
				require.True(t, ok)
				require.InDelta(t, decisionTimeout.Seconds(), time.Until(deadline).Seconds(), 5)

				return tc.answers, DecisionUsage{InputTokens: 2000}, tc.err
			}}
			tool := decisionTool(map[string]Decider{"openai": decider}, ObservabilityConfig{})

			subjects, err := tool.Subjects(json.RawMessage(testDecisionToolArgs))
			require.NoError(t, err)
			require.Equal(t, []string{"openai/gpt-6-luna"}, subjects)

			result, err := tool.Call(t.Context(), json.RawMessage(testDecisionToolArgs), nil, toolCallMetadata{})
			require.NoError(t, err)
			require.JSONEq(t, tc.want, result.Output)
			require.Equal(t, &DecisionRequest{Model: "gpt-6-luna", Input: map[string]any{"message": "Please refund me."}, Questions: []DecisionQuestion{
				{Name: "billing", Type: DecisionPredicate, Instructions: "Is it about billing?", Options: []DecisionOption{{Value: "true", Description: "Money"}, {Value: "false", Description: "Anything else"}}},
				{Name: "tone", Type: DecisionChoice, Instructions: "What is the tone?", Options: []DecisionOption{{Value: "angry", Description: "Upset"}, {Value: "calm", Description: "Neutral or polite"}}},
				{Name: "urgency", Type: DecisionScore, Instructions: "How urgent is it?", Options: []DecisionOption{{Value: "can wait"}, {Value: "today"}}},
			}}, decider.DecideCalls()[0].Request)
		})
	}

	tool := decisionTool(map[string]Decider{"openai": &mockDecider{}}, ObservabilityConfig{})

	result, err := tool.Call(t.Context(), json.RawMessage(`{"model":"typesafe/jev-latest","state":{},"questions":{}}`), nil, toolCallMetadata{})
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"typesafe/jev-latest","answers":{},"stop_reason":"error","error_message":"decision provider \"typesafe\" is not configured"}`, result.Output)

	for raw, wantErr := range map[string]string{
		`{"model":"openai/gpt-6-luna","state":{},"questions":{"q":{"type":"score","instructions":"?","criteria":{"a":"b"}}}}`: `question "q": a score's criteria list its levels, lowest first`,
		`{"model":"openai/gpt-6-luna","state":{},"questions":{"q":{"type":"rank","instructions":"?","criteria":[]}}}`:         `question "q": type must be choice, score or bool`,
	} {
		_, err := tool.Call(t.Context(), json.RawMessage(raw), nil, toolCallMetadata{})
		require.EqualError(t, err, wantErr)
	}
}

// Every decision call is a generation span carrying its tokens.
func TestDecideRecordsAGenerationSpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	decider := &mockDecider{DecideFunc: func(context.Context, *DecisionRequest) ([]DecisionAnswer, DecisionUsage, error) {
		return nil, DecisionUsage{InputTokens: 2000, OutputTokens: 3}, nil
	}}

	_, _, err := decide(t.Context(), ObservabilityConfig{Enabled: true, Tracer: provider.Tracer("test")}, map[string]Decider{"typesafe": decider}, "typesafe/jev-latest", &DecisionRequest{Input: "x"})
	require.NoError(t, err)

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	require.Equal(t, "rocketcode.decision", spans[0].Name())

	attrs := spans[0].Attributes()
	for _, want := range []attribute.KeyValue{
		otelsemconv.GenAIOperationNameChat,
		otelsemconv.GenAIRequestModel("jev-latest"),
		otelsemconv.GenAIProviderNameKey.String("typesafe"),
		otelsemconv.GenAIUsageInputTokensKey.Int64(2000),
		otelsemconv.GenAIUsageOutputTokensKey.Int64(3),
	} {
		require.Contains(t, attrs, want)
	}
}

// Code Mode scripts read the answers as a dict, with images read through the agent's read tool.
func TestExecuteDecisionReturnsADict(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll(".tmp/shell", 0o700))
	require.NoError(t, root.WriteFile("photo.png", []byte("\x89PNG\r\n\x1a\nrest"), 0o600))

	shellTemp := testShellTempConfig(t, root, filepath.Join(dir, ".tmp", "shell"))
	baseTools := makeSandboxedTools(&sandboxedFileSystem{mu: sync.Mutex{}, root: root}, newSandboxedShellSystem(root, &shellTemp, nil, DefaultShellCommand{}, &sandbox.Runtime{}))

	decider := &mockDecider{DecideFunc: func(_ context.Context, request *DecisionRequest) ([]DecisionAnswer, DecisionUsage, error) {
		require.Equal(t, []DecisionImage{{MIME: "image/png", Base64: base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nrest"))}}, request.Images)

		return []DecisionAnswer{{Name: "tone", Type: DecisionChoice, Choice: "calm", Confidence: 0.7}}, DecisionUsage{InputTokens: 12}, nil
	}}
	baseTools["decision"] = decisionTool(map[string]Decider{"openai": decider}, ObservabilityConfig{})

	run := func(permissions PermissionSet) (ToolResult, error) {
		factory := &toolFactory{baseTools: baseTools}
		model, hosts := factory.assembleTools(&Agent{Permission: permissions})
		looper := &looper{Journal: InertJournal{}, observations: &turnObservations{journal: InertJournal{}}, Permissions: permissions, Tools: model, CodeModeHosts: hosts}
		ctx := context.WithValue(t.Context(), toolCallContextKey{}, toolCallContext{looper: looper, observations: looper.observations, output: nil, callID: ""})

		raw, err := json.Marshal(struct {
			Code string `json:"code"`
		}{"def main():\n    r = decision(model=\"openai/gpt-6-luna\", state={\"message\": \"hi\"}, images=[\"photo.png\"], questions={\"tone\": {\"type\": \"choice\", \"instructions\": \"Tone?\", \"criteria\": {\"calm\": \"\", \"angry\": \"\"}}})\n    return \"%s %s %d\" % (r[\"answers\"][\"tone\"][\"choice\"], r[\"stop_reason\"], r[\"usage\"][\"input_tokens\"])\n"})
		require.NoError(t, err)

		return model[executeToolName].Call(ctx, raw, nil, emptyToolCallMetadata())
	}

	var permissions PermissionSet
	require.NoError(t, permissions.Allow("decision", "openai/*"))
	require.NoError(t, permissions.Allow("read", "*"))

	result, err := run(permissions)
	require.NoError(t, err)
	require.Equal(t, "calm stop 12", result.Output)

	// Images are files, so they need the agent's read permission.
	var noRead PermissionSet
	require.NoError(t, noRead.Allow("decision", "openai/*"))

	_, err = run(noRead)
	require.ErrorContains(t, err, "images: reading files needs read permission")
}

// roundConfidence rounds each confidence to two places, since a predicate's is computed in floating point.
func roundConfidence(answers []DecisionAnswer) []DecisionAnswer {
	for i := range answers {
		answers[i].Confidence = float64(int(answers[i].Confidence*100+0.5)) / 100
	}

	return answers
}
