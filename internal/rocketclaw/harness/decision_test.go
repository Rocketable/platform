package harness

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	otelsemconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.starlark.net/starlark"
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
				require.True(t, strings.HasPrefix(tool.Definition.Description.Value, decisionToolSummary+" Models you may call: openai/*.\nstate is a dict (not a string) of the data the questions are about.\n"))
				require.Contains(t, codeModeSystemPrompt(hosts, nil), "- decision(images=..., model=..., questions=..., state=...) // Ask a decision model typed questions on a state dict; search first for the format. Models you may call: openai/*.")
			}
		})
	}

	for _, deciders := range []map[string]Decider{nil, {"openai": &mockDecider{}}} {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, root.Close()) })

		client := openai.NewClient()
		config := testWorkspaceConfig(t, dir)
		config.Deciders = deciders
		loop, err := NewWithModelResolver(testResolverForClient(&client), config, root, Agents{Items: map[string]Agent{"main": {Name: "main", Model: "gpt-5.4", Permission: PermissionSet{Sudo: true}}}}, Skills{Items: map[string]Skill{}}, "main")
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
		{name: "refusal", answers: answers[:2], want: `{"model":"openai/gpt-6-luna","answers":{},"usage":{"input_tokens":2000,"output_tokens":0},"stop_reason":"error","error_message":"the model did not answer \"urgency\""}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decider := &mockDecider{DecideFunc: func(ctx context.Context, _ *DecisionRequest) ([]DecisionAnswer, DecisionUsage, error) {
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

func TestExecuteDecisionReturnsADict(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll(".tmp/shell", 0o700))
	require.NoError(t, root.WriteFile("photo.png", []byte("\x89PNG\r\n\x1a\nrest"), 0o600))

	shellTemp := testShellTempConfig(t, root, filepath.Join(dir, ".tmp", "shell"))
	baseTools := makeSandboxedTools(&sandboxedFileSystem{mu: sync.Mutex{}, root: root}, newSandboxedShellSystem(root, &shellTemp, nil, &sandbox.Runtime{}))

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
		}{"def main():\n    r = decision(model=\"openai/gpt-6-luna\", state={\"message\": \"hi\"}, images=[\"photo.png\"], questions={\"tone\": {\"type\": \"choice\", \"instructions\": \"Tone?\", \"criteria\": {\"calm\": \"\", \"angry\": \"\"}}})\n    if not r.ok:\n        return r.error\n    return \"%s %s %d\" % (r[\"answers\"][\"tone\"][\"choice\"], r[\"stop_reason\"], r[\"usage\"][\"input_tokens\"])\n"})
		require.NoError(t, err)

		return model[executeToolName].Call(ctx, raw, nil, emptyToolCallMetadata())
	}

	var permissions PermissionSet
	require.NoError(t, permissions.Set("decision", "openai/*", PermissionAllow))
	require.NoError(t, permissions.Set("read", "*", PermissionAllow))

	result, err := run(permissions)
	require.NoError(t, err)
	require.Equal(t, "calm stop 12", result.Output)

	var noRead PermissionSet
	require.NoError(t, noRead.Set("decision", "openai/*", PermissionAllow))

	result, err = run(noRead)
	require.NoError(t, err)
	require.Contains(t, result.Output, "images: reading files needs read permission")
}

func TestExecuteDecisionTakesImageFileObjects(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll(".tmp/shell", 0o700))

	onDisk := "\x89PNG\r\n\x1a\nrest"
	require.NoError(t, root.WriteFile("photo.png", []byte(onDisk), 0o600))

	encodePNG := func(width, height int) string {
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, height))))

		return buf.String()
	}
	photo := codemode.NewFile(codemode.FileSpec{Name: "photo.png", Mime: "image/png", Content: encodePNG(2, 2), Source: "read photo.png"})
	thumb := codemode.NewFile(codemode.FileSpec{Name: "thumb.png", Mime: "image/png", Content: encodePNG(1, 1), Source: "read thumb.png"})
	notes := codemode.NewFile(codemode.FileSpec{Name: "notes.pdf", Mime: "application/pdf", Content: "%PDF-1.4", Source: "read notes.pdf"})

	pngImage := func(content string) DecisionImage {
		return DecisionImage{MIME: "image/png", Base64: base64.StdEncoding.EncodeToString([]byte(content))}
	}
	fileArgError := func(tool, arg, name string) *codemode.Error {
		return codemode.NewFailure(tool, fmt.Sprintf("argument %s got a file object; pass %s.base64(), a path, or text", arg, strings.TrimSuffix(name, filepath.Ext(name))), nil)
	}

	shellTemp := testShellTempConfig(t, root, filepath.Join(dir, ".tmp", "shell"))
	baseTools := makeSandboxedTools(&sandboxedFileSystem{mu: sync.Mutex{}, root: root}, newSandboxedShellSystem(root, &shellTemp, nil, &sandbox.Runtime{}))

	var permissions PermissionSet
	require.NoError(t, permissions.Set("decision", "openai/*", PermissionAllow))
	require.NoError(t, permissions.Set("read", "*", PermissionAllow))

	for _, tc := range []struct {
		name       string
		call       string
		reviewed   []any
		wantImages []DecisionImage
		wantError  *codemode.Error
	}{
		{name: "path", call: `decision(images=["photo.png"], state={}, ...)`, reviewed: []any{"photo.png"}, wantImages: []DecisionImage{pngImage(onDisk)}},
		{name: "file object", call: `decision(images=[photo], state={}, ...)`, reviewed: []any{photo}, wantImages: []DecisionImage{pngImage(photo.Content())}},
		{name: "second file object", call: `decision(images=(thumb,), state={}, ...)`, reviewed: []any{thumb}, wantImages: []DecisionImage{pngImage(thumb.Content())}},
		{name: "mixed", call: `decision(images=[thumb, "photo.png", photo], state={}, ...)`, reviewed: []any{thumb, "photo.png", photo}, wantImages: []DecisionImage{pngImage(thumb.Content()), pngImage(onDisk), pngImage(photo.Content())}},
		{name: "non-image file object", call: `decision(images=[photo, notes], state={}, ...)`, wantError: codemode.NewFailure("decision", `image "notes.pdf": not an image (application/pdf)`, nil)},
		{name: "descriptor without its file", call: `decision(images=[{"sha256": photo.sha256}], state={}, ...)`, wantError: codemode.NewFailure("decision", `image {"sha256":"`+photo.SHA256()+`"}: pass a path or a file object`, nil)},
		{name: "images not a list", call: `decision(images=photo, state={}, ...)`, wantError: fileArgError("decision", "images", "photo.png")},
		{name: "images nested list", call: `decision(images=[[photo]], state={}, ...)`, wantError: fileArgError("decision", "images", "photo.png")},
		{name: "images nested dict", call: `decision(images=[{"pic": photo}], state={}, ...)`, wantError: fileArgError("decision", "images", "photo.png")},
		{name: "state", call: `decision(state={"pics": [notes]}, ...)`, wantError: fileArgError("decision", "state", "notes.pdf")},
		{name: "host tool", call: `read(filePath=photo)`, wantError: fileArgError("read", "filePath", "photo.png")},
		{name: "mcp tool", call: `figma_upload(image={"files": [thumb]})`, wantError: fileArgError("figma.upload", "image", "thumb.png")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decider := &mockDecider{DecideFunc: func(context.Context, *DecisionRequest) ([]DecisionAnswer, DecisionUsage, error) {
				return []DecisionAnswer{{Name: "tone", Type: DecisionChoice, Choice: "calm"}}, DecisionUsage{}, nil
			}}
			decision := decisionTool(map[string]Decider{"openai": decider}, ObservabilityConfig{})
			callDecision := decision.Call

			var reviewed json.RawMessage

			decision.Call = func(ctx context.Context, raw json.RawMessage, output chan<- ChatResponse, metadata toolCallMetadata) (ToolResult, error) {
				reviewed = raw

				return callDecision(ctx, raw, output, metadata)
			}

			baseTools["decision"] = decision
			model, hosts := (&toolFactory{baseTools: baseTools}).assembleTools(&Agent{Permission: permissions})
			looper := &looper{Journal: InertJournal{}, observations: &turnObservations{journal: InertJournal{}}, Permissions: permissions, Tools: model, CodeModeHosts: hosts}
			ctx := context.WithValue(t.Context(), toolCallContextKey{}, toolCallContext{looper: looper, observations: looper.observations, output: nil, callID: ""})

			host, _ := codeModeHostToolsFromContext(ctx)
			host = append(host, codemode.HostTool{Details: codemode.NoDetails{}, Name: "files", CallValue: func(context.Context, map[string]any) (starlark.Value, error) {
				return starlark.Tuple{photo, thumb, notes}, nil
			}})
			mcp := []codemode.ToolDesc{{Server: "figma", Name: "upload"}}
			script := "def main():\n    photo, thumb, notes = files()\n    return " + strings.ReplaceAll(tc.call, "...", `model="openai/gpt-6-luna", questions={"tone": {"type": "choice", "instructions": "Tone?", "criteria": {"calm": "", "angry": ""}}}`) + "\n"

			value, err := codemode.Run(ctx, script, mcp, func(context.Context, string, map[string]any) error { return nil }, func(context.Context, string, string, map[string]any) (starlark.Value, error) {
				return nil, errors.New("the call should have been refused")
			}, host)
			require.NoError(t, err)

			if tc.wantError != nil {
				require.Equal(t, []any{"error", tc.wantError.String()}, []any{value.Type(), value.String()})
				require.Empty(t, decider.DecideCalls())

				return
			}

			result, ok := value.(*codemode.DictResult)
			require.True(t, ok, "got %s", value.Type())
			require.JSONEq(t, `{"model": "openai/gpt-6-luna", "answers": {"tone": {"type": "choice", "choice": "calm", "confidence": 0}}, "usage": {"input_tokens": 0, "output_tokens": 0}, "stop_reason": "stop"}`, result.String())
			require.Equal(t, tc.wantImages, decider.DecideCalls()[0].Request.Images)

			var args struct {
				Images json.RawMessage `json:"images"`
			}
			require.NoError(t, json.Unmarshal(reviewed, &args))

			want, err := json.Marshal(tc.reviewed)
			require.NoError(t, err)
			require.JSONEq(t, string(want), string(args.Images))
		})
	}
}

func roundConfidence(answers []DecisionAnswer) []DecisionAnswer {
	for i := range answers {
		answers[i].Confidence = float64(int(answers[i].Confidence*100+0.5)) / 100
	}

	return answers
}
