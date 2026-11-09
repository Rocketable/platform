package harness

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"

	semconv "github.com/Arize-ai/openinference/go/openinference-semantic-conventions"
	openai "github.com/openai/openai-go/v3"
	"go.opentelemetry.io/otel/attribute"
	otelsemconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

// DecisionQuestionType names a decision question's kind.
type DecisionQuestionType string

// The decision question kinds every provider answers.
const (
	DecisionPredicate DecisionQuestionType = "predicate"
	DecisionChoice    DecisionQuestionType = "choice"
	DecisionScore     DecisionQuestionType = "score"
)

// DecisionOption is one of a question's options: a choice, a score level, or a predicate's "true" or
// "false". Description, which may be empty, says when it applies.
type DecisionOption struct {
	Value       string
	Description string
}

// DecisionQuestion is one typed question: a yes/no predicate, a choice among Options, or a score
// over Options ordered from lowest to highest.
type DecisionQuestion struct {
	Name         string
	Type         DecisionQuestionType
	Instructions string
	Options      []DecisionOption
}

// DecisionRequest asks Questions about Input, a string or a JSON object, and Images, under the trusted
// Instructions, which may be empty.
type DecisionRequest struct {
	Model        string
	Instructions string
	Input        any
	Images       []DecisionImage
	Questions    []DecisionQuestion
}

// DecisionImage is an image judged with a request's input: its MIME type and base64 data.
type DecisionImage struct {
	MIME   string
	Base64 string
}

// DecisionUsage is the token count a provider bills for one decision request.
type DecisionUsage struct {
	InputTokens  int64
	OutputTokens int64
}

// DecisionAnswer is a provider's answer to the question named Name. A predicate sets Probability
// (that it is true); a choice sets Choice; a score sets Score, its expected level index, and Choice,
// its most probable level. Confidence runs from 0 to 1; a predicate's is its distance from 0.5,
// doubled. Probabilities are keyed by option. Refused means the model declined the question.
type DecisionAnswer struct {
	Name          string
	Type          DecisionQuestionType
	Refused       bool
	Probability   float64
	Choice        string
	Score         float64
	Confidence    float64
	Probabilities map[string]float64
}

// Decider answers decision questions on one provider, in question order, and reports the tokens billed.
type Decider interface {
	Decide(ctx context.Context, request *DecisionRequest) ([]DecisionAnswer, DecisionUsage, error)
}

// decide asks ref, a provider/model, through its provider's decider within decisionTimeout, and records
// the call as a generation span with its token usage.
func decide(ctx context.Context, observability ObservabilityConfig, deciders map[string]Decider, ref string, request *DecisionRequest) ([]DecisionAnswer, DecisionUsage, error) {
	provider, model, _ := strings.Cut(ref, "/")

	// A config reload can drop a provider an agent loaded earlier still names.
	decider, ok := deciders[provider]
	if !ok {
		return nil, DecisionUsage{}, fmt.Errorf("decision provider %q is not configured", provider)
	}

	request.Model = model

	ctx, cancel := context.WithTimeout(ctx, decisionTimeout)
	defer cancel()

	ctx, span := observability.startSpan(ctx, "rocketcode.decision", semconv.SpanKindLLM,
		attribute.String(semconv.LLMModelName, model),
		attribute.String(semconv.LLMProvider, provider),
		otelsemconv.GenAIRequestModel(model),
		otelsemconv.GenAIProviderNameKey.String(provider),
	)
	defer span.span.End()

	answers, usage, err := decider.Decide(ctx, request)
	if err != nil {
		recordSpanError(span, err)

		return nil, DecisionUsage{}, fmt.Errorf("decide with %s: %w", ref, err)
	}

	span.span.SetAttributes(TokenUsage{PromptTokens: usage.InputTokens, CompletionTokens: usage.OutputTokens, TotalTokens: usage.InputTokens + usage.OutputTokens}.attributes()...)

	return answers, usage, nil
}

// decisionToolSummary opens the decision tool's description, which Code Mode's catalog shows alone.
const decisionToolSummary = "Ask a decision model typed questions about state and get answers with probabilities."

func decisionToolDescription(models []string) string {
	summary := decisionToolSummary
	if len(models) > 0 {
		summary += " Models you may call: " + strings.Join(models, ", ") + "."
	}

	return strings.Join([]string{
		summary,
		"questions maps each name to {type, instructions, criteria}: a choice's criteria map each label to when it applies; a score's criteria list its levels, lowest first; a bool's criteria are {\"true\": ..., \"false\": ...}.",
		"images, which may be empty, lists workspace image files judged with the state; reading them needs read permission.",
		"Returns {model, answers, usage, stop_reason, error_message}, with answers by question name: a choice {choice, probabilities, confidence}, a score {score, confidence}, where score is the expected level index, and a bool {probability}, that it is true. usage is {input_tokens, output_tokens}.",
		"Confidence runs from 0 to 1; act on an answer only when it is high enough for what depends on it.",
		"A provider error, a refused question or 30 seconds without an answer returns stop_reason \"error\" and error_message instead of failing the script.",
		"Each call judges one state; use gather to judge many.",
	}, "\n")
}

// decisionToolResult is what a Code Mode script gets back from the decision tool.
type decisionToolResult struct {
	Model        string                        `json:"model"`
	Answers      map[string]decisionToolAnswer `json:"answers"`
	Usage        *decisionToolUsage            `json:"usage,omitempty"`
	StopReason   string                        `json:"stop_reason"`
	ErrorMessage string                        `json:"error_message,omitempty"`
}

type decisionToolUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type decisionToolAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probability   *float64           `json:"probability,omitempty"`
}

// decisionToolQuestion is one question a Code Mode script asks.
type decisionToolQuestion struct {
	Type         string          `json:"type"`
	Instructions string          `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria"`
}

// decisionQuestions turns a script's questions into decision questions, sorted by name.
func decisionQuestions(questions map[string]decisionToolQuestion) ([]DecisionQuestion, error) {
	out := make([]DecisionQuestion, 0, len(questions))

	for _, name := range slices.Sorted(maps.Keys(questions)) {
		question := questions[name]
		decision := DecisionQuestion{Name: name, Instructions: question.Instructions}

		switch question.Type {
		case "choice":
			var criteria map[string]string
			if err := json.Unmarshal(question.Criteria, &criteria); err != nil || len(criteria) == 0 {
				return nil, fmt.Errorf("question %q: a choice's criteria map each label to when it applies", name)
			}

			decision.Type = DecisionChoice
			for _, label := range slices.Sorted(maps.Keys(criteria)) {
				decision.Options = append(decision.Options, DecisionOption{Value: label, Description: criteria[label]})
			}
		case "score":
			var levels []string
			if err := json.Unmarshal(question.Criteria, &levels); err != nil || len(levels) == 0 {
				return nil, fmt.Errorf("question %q: a score's criteria list its levels, lowest first", name)
			}

			decision.Type = DecisionScore
			for _, level := range levels {
				decision.Options = append(decision.Options, DecisionOption{Value: level})
			}
		case "bool":
			var criteria struct {
				True  string `json:"true"`
				False string `json:"false"`
			}
			if err := json.Unmarshal(question.Criteria, &criteria); err != nil {
				return nil, fmt.Errorf("question %q: a bool's criteria are {\"true\": ..., \"false\": ...}", name)
			}

			decision.Type = DecisionPredicate
			decision.Options = []DecisionOption{{Value: "true", Description: criteria.True}, {Value: "false", Description: criteria.False}}
		default:
			return nil, fmt.Errorf("question %q: type must be choice, score or bool", name)
		}

		out = append(out, decision)
	}

	return out, nil
}

// decisionToolAnswers maps answers back to the script's question names, or reports the first question
// left unanswered.
func decisionToolAnswers(questions []DecisionQuestion, answers []DecisionAnswer) (map[string]decisionToolAnswer, error) {
	byName := make(map[string]DecisionAnswer, len(answers))
	for _, answer := range answers {
		byName[answer.Name] = answer
	}

	out := make(map[string]decisionToolAnswer, len(questions))

	for _, question := range questions {
		answer, ok := byName[question.Name]
		if !ok || answer.Refused {
			return nil, fmt.Errorf("the model did not answer %q", question.Name)
		}

		switch question.Type {
		case DecisionChoice:
			out[question.Name] = decisionToolAnswer{Type: "choice", Choice: answer.Choice, Probabilities: answer.Probabilities, Confidence: &answer.Confidence}
		case DecisionScore:
			out[question.Name] = decisionToolAnswer{Type: "score", Score: &answer.Score, Confidence: &answer.Confidence}
		case DecisionPredicate:
			out[question.Name] = decisionToolAnswer{Type: "bool", Probability: &answer.Probability}
		}
	}

	return out, nil
}

// decisionImages reads paths, workspace image files, through the agent's own read tool and its
// permission rules.
func decisionImages(ctx context.Context, paths []string) ([]DecisionImage, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	tc, _ := toolCallContextFrom(ctx)

	read, ok := tc.looper.CodeModeHosts["read"]
	if !ok {
		return nil, errors.New("images: reading files needs read permission")
	}

	images := make([]DecisionImage, 0, len(paths))

	for _, path := range paths {
		raw, _ := json.Marshal(struct {
			FilePath string `json:"filePath"`
		}{path}) // A struct of one string field always encodes.

		if err := CheckNestedToolCall(ctx, "read", &read, raw); err != nil {
			return nil, fmt.Errorf("image %q: %w", path, err)
		}

		result, err := read.Call(ctx, raw, nil, toolCallMetadata{})
		if err != nil {
			return nil, fmt.Errorf("image %q: %w", path, err)
		}

		if len(result.Attachments) == 0 || !strings.HasPrefix(result.Attachments[0].MIME, "image/") {
			return nil, fmt.Errorf("image %q: %s", path, cmp.Or(result.Output, "not an image"))
		}

		mime, data, _ := strings.Cut(strings.TrimPrefix(result.Attachments[0].URL, "data:"), ";base64,")
		images = append(images, DecisionImage{MIME: mime, Base64: data})
	}

	return images, nil
}

// decisionTool asks a decision model typed questions from Code Mode. Its permission subject is the
// provider/model it calls.
func decisionTool(deciders map[string]Decider, observability ObservabilityConfig) looperTool {
	type params struct {
		Model     string                          `json:"model"`
		State     map[string]any                  `json:"state"`
		Images    []string                        `json:"images"`
		Questions map[string]decisionToolQuestion `json:"questions"`
	}

	return looperTool{
		Definition: *functionTool("decision", decisionToolDescription(nil), map[string]any{
			"model":  map[string]any{"type": "string", "description": "provider/model, such as typesafe/jev-latest."},
			"state":  map[string]any{"type": "object", "description": "The data every question is about."},
			"images": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Workspace image files judged with the state."},
			"questions": map[string]any{"type": "object", "description": "Questions by name; one call answers all of them.", "additionalProperties": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"type":         map[string]any{"type": "string", "enum": []string{"choice", "score", "bool"}},
					"instructions": map[string]any{"type": "string"},
					"criteria":     map[string]any{"description": "A choice's {label: when it applies}, a score's [levels, lowest first], or a bool's {\"true\": ..., \"false\": ...}."},
				},
				"required":             []string{"type", "instructions", "criteria"},
				"additionalProperties": false,
			}},
		}),
		Permission:   "decision",
		codeModeOnly: true,
		Subjects: func(raw json.RawMessage) ([]string, error) {
			var input params

			err := decodeToolParams(raw, &input)

			return []string{input.Model}, err
		},
		Call: func(ctx context.Context, raw json.RawMessage, _ chan<- ChatResponse, _ toolCallMetadata) (ToolResult, error) {
			var input params
			if err := decodeToolParams(raw, &input); err != nil {
				return ToolResult{}, err
			}

			questions, err := decisionQuestions(input.Questions)
			if err != nil {
				return ToolResult{}, err
			}

			images, err := decisionImages(ctx, input.Images)
			if err != nil {
				return ToolResult{}, err
			}

			result := decisionToolResult{Model: input.Model, Answers: map[string]decisionToolAnswer{}, StopReason: "stop"}

			answers, usage, err := decide(ctx, observability, deciders, input.Model, &DecisionRequest{Input: input.State, Images: images, Questions: questions})
			if err == nil {
				// A request whose answers fail below was still billed.
				result.Usage = &decisionToolUsage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens}

				result.Answers, err = decisionToolAnswers(questions, answers)
			}

			// Starlark cannot catch errors, so a failed answer is a result the script can check.
			if err != nil {
				result.Answers, result.StopReason, result.ErrorMessage = map[string]decisionToolAnswer{}, "error", err.Error()
			}

			output, err := json.Marshal(result)

			return TextToolResult(string(output)), err
		},
	}
}

func predicateAnswer(name string, probability float64) DecisionAnswer {
	return DecisionAnswer{Name: name, Type: DecisionPredicate, Probability: probability, Confidence: math.Abs(2*probability - 1)}
}

// OpenAIDecider answers through OpenAI's Decisions API.
type OpenAIDecider struct {
	Client *openai.Client
}

// Decide implements Decider.
func (d OpenAIDecider) Decide(ctx context.Context, request *DecisionRequest) ([]DecisionAnswer, DecisionUsage, error) {
	input := []openai.DecisionNewParamsInputArrayItemUnion{}

	if request.Instructions != "" {
		developer := openai.DecisionInputMessageParamOfDeveloper(request.Instructions)
		input = append(input, openai.DecisionNewParamsInputArrayItemUnion{OfDecisionInputMessage: &developer})
	}

	text, ok := request.Input.(string)
	if !ok {
		encoded, err := json.Marshal(request.Input)
		if err != nil {
			return nil, DecisionUsage{}, fmt.Errorf("openai decision: %w", err)
		}

		text = string(encoded)
	}

	user := openai.DecisionInputMessageParamOfUser(text)

	if len(request.Images) > 0 {
		// The endpoint takes at most this many images.
		if len(request.Images) > 128 {
			return nil, DecisionUsage{}, fmt.Errorf("openai decision: at most 128 images, got %d", len(request.Images))
		}

		parts := []openai.DecisionInputPartUnionParam{openai.DecisionInputPartParamOfInputText(text)}
		for _, image := range request.Images {
			parts = append(parts, openai.DecisionInputPartParamOfInputImage("data:"+image.MIME+";base64,"+image.Base64))
		}

		user = openai.DecisionInputMessageParamOfUser(parts)
	}

	params := openai.DecisionNewParams{Model: request.Model, Input: openai.DecisionNewParamsInputUnion{OfDecisionNewsInputArray: append(input, openai.DecisionNewParamsInputArrayItemUnion{OfDecisionInputMessage: &user})}}

	for _, question := range request.Questions {
		name, instructions := openai.String(question.Name), question.Instructions

		switch question.Type {
		case DecisionPredicate:
			// Predicates have no criteria, so what true and false mean follows the instructions.
			lines := []string{instructions}

			for _, option := range question.Options {
				if option.Description != "" {
					lines = append(lines, option.Value+" means: "+option.Description)
				}
			}

			instructions = strings.Join(lines, "\n")

			params.Questions = append(params.Questions, openai.DecisionNewParamsQuestionUnion{OfPredicate: &openai.DecisionNewParamsQuestionPredicate{Name: name, Instructions: openai.DecisionNewParamsQuestionPredicateInstructionsUnion{OfString: openai.String(instructions)}}})
		case DecisionChoice:
			choice := &openai.DecisionNewParamsQuestionChoice{Name: name, Instructions: openai.DecisionNewParamsQuestionChoiceInstructionsUnion{OfString: openai.String(instructions)}}
			for _, option := range question.Options {
				choiceParam := openai.DecisionNewParamsQuestionChoiceChoice{Value: openai.DecisionNewParamsQuestionChoiceChoiceValueUnion{OfString: openai.String(option.Value)}}
				if option.Description != "" {
					choiceParam.Description = openai.String(option.Description)
				}

				choice.Choices = append(choice.Choices, choiceParam)
			}

			params.Questions = append(params.Questions, openai.DecisionNewParamsQuestionUnion{OfChoice: choice})
		case DecisionScore:
			score := &openai.DecisionNewParamsQuestionScore{Name: name, Instructions: openai.DecisionNewParamsQuestionScoreInstructionsUnion{OfString: openai.String(instructions)}}
			for _, option := range question.Options {
				score.Levels = append(score.Levels, openai.DecisionNewParamsQuestionScoreLevel{Label: option.Value})
			}

			params.Questions = append(params.Questions, openai.DecisionNewParamsQuestionUnion{OfScore: score})
		}
	}

	decision, err := d.Client.Decisions.New(ctx, params)
	if err != nil {
		return nil, DecisionUsage{}, fmt.Errorf("openai decision: %w", err)
	}

	answers := make([]DecisionAnswer, 0, len(decision.Answers))
	for i := range decision.Answers {
		raw := &decision.Answers[i]
		answer := DecisionAnswer{Name: raw.Name, Type: DecisionQuestionType(raw.Type), Confidence: raw.Confidence, Probabilities: map[string]float64{}}

		switch answer.Type {
		case DecisionPredicate:
			answer = predicateAnswer(raw.Name, raw.Probability)
		case DecisionChoice:
			answer.Choice = raw.Choice.OfString
			for j := range raw.Probabilities.OfDecisionAnswerChoiceProbabilities {
				p := &raw.Probabilities.OfDecisionAnswerChoiceProbabilities[j]
				answer.Probabilities[p.Value.OfString] = p.Probability
			}
		case DecisionScore:
			answer.Score = raw.Score
			for j := range raw.Probabilities.OfDecisionAnswerScoreProbabilities {
				p := &raw.Probabilities.OfDecisionAnswerScoreProbabilities[j]
				answer.Probabilities[p.Label] = p.Probability

				if answer.Choice == "" || p.Probability > answer.Probabilities[answer.Choice] {
					answer.Choice = p.Label
				}
			}
		default:
			answer = DecisionAnswer{Name: raw.Name, Refused: true}
		}

		answers = append(answers, answer)
	}

	return answers, DecisionUsage{InputTokens: decision.Usage.InputTokens, OutputTokens: decision.Usage.OutputTokens}, nil
}

// typeSafeCriteria returns what System One reads a question's options from: a predicate's meanings of
// true and false, when given, a choice's options by name with their descriptions, or a score's levels.
func typeSafeCriteria(question *DecisionQuestion) any {
	switch question.Type {
	case DecisionChoice:
		// A choice without a description is read by its name alone.
		criteria := make(map[string]*string, len(question.Options))
		for _, option := range question.Options {
			criteria[option.Value] = nil
			if option.Description != "" {
				criteria[option.Value] = &option.Description
			}
		}

		return criteria
	case DecisionScore:
		levels := make([]string, 0, len(question.Options))
		for _, option := range question.Options {
			levels = append(levels, option.Value)
		}

		return levels
	case DecisionPredicate:
		criteria := map[string]string{}

		for _, option := range question.Options {
			if option.Description != "" {
				criteria[option.Value] = option.Description
			}
		}

		if len(criteria) > 0 {
			return criteria
		}
	}

	return nil
}

// TypeSafeDecider answers through the System One API at BaseURL, which includes the API version, such as
// https://api.typesafe.ai/v1; another service speaking the same protocol works too.
type TypeSafeDecider struct {
	HTTPClient *http.Client
	APIKey     string
	BaseURL    string
}

// Decide implements Decider.
func (d TypeSafeDecider) Decide(ctx context.Context, request *DecisionRequest) ([]DecisionAnswer, DecisionUsage, error) {
	type typeSafeQuestion struct {
		Type         string `json:"type"`
		Instructions any    `json:"instructions"`
		// Criteria describe a predicate's true and false, a choice's options or a score's levels.
		Criteria any `json:"criteria,omitempty"`
	}

	questions := make(map[string]typeSafeQuestion, len(request.Questions))
	for _, question := range request.Questions {
		body := typeSafeQuestion{Type: string(question.Type), Instructions: question.Instructions}
		// System One has no request-level instructions, and state is the content being judged, so trusted
		// rules travel with each question.
		if request.Instructions != "" {
			body.Instructions = struct {
				Rules    string `json:"rules"`
				Question string `json:"question"`
			}{request.Instructions, question.Instructions}
		}

		if question.Type == DecisionPredicate {
			body.Type = "noul"
		}

		body.Criteria = typeSafeCriteria(&question)
		questions[question.Name] = body
	}

	type typeSafeAsset struct {
		Type       string `json:"type"`
		Format     string `json:"format"`
		DataBase64 string `json:"data_base64"`
	}

	// Each image is an asset, named before the state by a reference that cannot occur in text by accident.
	state, assets := request.Input, map[string]typeSafeAsset{}
	if len(request.Images) > 0 {
		references := make([]any, 0, len(request.Images)+1)

		for _, image := range request.Images {
			format, ok := map[string]string{"image/png": "png", "image/jpeg": "jpg"}[image.MIME]
			if !ok {
				return nil, DecisionUsage{}, fmt.Errorf("typesafe decision: images must be PNG or JPEG, got %s", image.MIME)
			}

			reference := "image-" + rand.Text()
			assets[reference] = typeSafeAsset{Type: "image", Format: format, DataBase64: image.Base64}
			references = append(references, reference)
		}

		references = append(references, request.Input)
		state = references
	}

	payload, err := json.Marshal(struct {
		Model     string                      `json:"model"`
		State     any                         `json:"state"`
		Questions map[string]typeSafeQuestion `json:"questions"`
		Assets    map[string]typeSafeAsset    `json:"assets,omitempty"`
	}{request.Model, state, questions, assets})
	if err != nil {
		return nil, DecisionUsage{}, fmt.Errorf("typesafe decision: %w", err)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(d.BaseURL, "/")+"/systemone", bytes.NewReader(payload))
	if err != nil {
		return nil, DecisionUsage{}, fmt.Errorf("typesafe decision: %w", err)
	}

	httpRequest.Header.Set("Authorization", "Bearer "+d.APIKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	// Review transcripts often exceed the default context.
	httpRequest.Header.Set("Typesafe-Danger-Zone-Long-Context", "true")

	if len(assets) > 0 {
		httpRequest.Header.Set("Typesafe-Danger-Zone-Images", "true")
	}

	response, err := d.HTTPClient.Do(httpRequest)
	if err != nil {
		return nil, DecisionUsage{}, fmt.Errorf("typesafe decision: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, DecisionUsage{}, fmt.Errorf("typesafe decision: %w", err)
	}

	if response.StatusCode != http.StatusOK {
		return nil, DecisionUsage{}, fmt.Errorf("typesafe decision: status %d: %s", response.StatusCode, string(body[:min(len(body), 512)]))
	}

	var decoded struct {
		Answers map[string]struct {
			Type          string             `json:"type"`
			Noul          float64            `json:"noul"`
			Choice        string             `json:"choice"`
			Score         float64            `json:"score"`
			Confidence    float64            `json:"confidence"`
			Probabilities map[string]float64 `json:"probabilities"`
		} `json:"answers"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, DecisionUsage{}, fmt.Errorf("typesafe decision: %w", err)
	}

	usage := DecisionUsage{InputTokens: decoded.Usage.InputTokens, OutputTokens: decoded.Usage.OutputTokens}

	answers := make([]DecisionAnswer, 0, len(request.Questions))
	for _, question := range request.Questions {
		raw, ok := decoded.Answers[question.Name]

		switch {
		case ok && raw.Type == "noul":
			answers = append(answers, predicateAnswer(question.Name, raw.Noul))
		case ok && raw.Type == "choice":
			answers = append(answers, DecisionAnswer{Name: question.Name, Type: DecisionChoice, Choice: raw.Choice, Confidence: raw.Confidence, Probabilities: raw.Probabilities})
		case ok && raw.Type == "score":
			answer := DecisionAnswer{Name: question.Name, Type: DecisionScore, Score: raw.Score, Confidence: raw.Confidence, Probabilities: map[string]float64{}}
			for key, probability := range raw.Probabilities {
				// Score probabilities are keyed by level index.
				level, err := strconv.Atoi(key)
				if err != nil || level < 0 || level >= len(question.Options) {
					return nil, usage, fmt.Errorf("typesafe decision: answer %q has unknown score level %q", question.Name, key)
				}

				label := question.Options[level].Value

				answer.Probabilities[label] = probability
				if answer.Choice == "" || probability > answer.Probabilities[answer.Choice] {
					answer.Choice = label
				}
			}

			answers = append(answers, answer)
		default:
			answers = append(answers, DecisionAnswer{Name: question.Name, Refused: true})
		}
	}

	return answers, usage, nil
}
