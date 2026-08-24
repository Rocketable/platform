package rocketcode

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"golang.org/x/sync/errgroup"
)

func (l *looper) prepareTurn(ctx context.Context, input PromptInput, output chan<- ChatResponse) (PromptInput, []responses.ResponseInputItemUnionParam, error) {
	input, turnItems, err := l.promptTurnItems(ctx, input)
	if err != nil {
		return input, nil, err
	}

	if err := l.bindModelRouter(ctx, input.Text, output); err != nil {
		return input, nil, err
	}

	return input, turnItems, nil
}

func (l *looper) bindModelRouter(ctx context.Context, message string, parentOutput chan<- ChatResponse) error {
	if l.agent.ModelRouter == "" || l.factory == nil {
		return nil
	}

	pick, err := l.factory.runModelRouter(ctx, &l.agent, message, parentOutput)
	if err != nil {
		return err
	}

	client, origin, err := resolveModel(l.factory.resolver, pick.Model)
	if err != nil {
		return fmt.Errorf("model router pick: %w", err)
	}

	l.Client = newResponsesAPI(client)
	l.ProviderOrigin = origin
	l.Model = origin.Model
	l.DisplayModel = origin.displayModel()
	l.ReasoningEffort = shared.ReasoningEffort(pick.ReasoningEffort)
	l.Verbosity = pick.Verbosity

	return nil
}

func (f *toolFactory) runModelRouter(ctx context.Context, routed *Agent, message string, parentOutput chan<- ChatResponse) (ModelOption, error) {
	agent, ok := f.agents.Items[routed.ModelRouter]
	if !ok {
		return ModelOption{}, fmt.Errorf("model router %q is missing", routed.ModelRouter)
	}

	agent.Permission = f.shellTemp.effectivePermissions(agent.Permission)
	expandAgentPrompt(ctx, &agent, f.expandPromptShellCommands.SubagentPrompts, &f.promptExpansion)

	client, origin, err := resolveModel(f.resolver, agent.Model)
	if err != nil {
		return ModelOption{}, fmt.Errorf("model router model failed: %w", err)
	}

	childFactory := *f
	modelTools, codeHosts := childFactory.assembleTools(&agent)
	child := &looper{
		agent:                  agent,
		ProviderOrigin:         origin,
		Client:                 newResponsesAPI(client),
		SystemPrompt:           composeSystemPromptWithSkills(agent.Prompt, f.skills, &agent),
		Model:                  origin.Model,
		DisplayModel:           origin.displayModel(),
		ReasoningEffort:        shared.ReasoningEffort(cmp.Or(agent.ReasoningEffort, string(f.reasoningEffort))),
		Verbosity:              agent.Verbosity,
		CompactThreshold:       f.compactThreshold,
		CompactionSteering:     f.compactionSteering,
		ParallelToolCalls:      f.parallelToolCalls,
		ResponseFormat:         modelRouterResponseFormat(),
		Permissions:            agent.Permission,
		Tools:                  modelTools,
		CodeModeHosts:          codeHosts,
		Diagnostics:            f.diagnostics,
		AutoApprovePermissions: f.autoApprovePermissions,
		PermissionReviewer:     &childFactory,
		Observability:          f.observability,
		CheckpointSink:         InertCheckpointSink{},
	}

	output := make(chan ChatResponse)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: modelRouterPrompt(message, routed.ModelOptions), Responses: output}

	close(input)

	var (
		group errgroup.Group
		last  string
	)

	group.Go(func() error {
		for item := range output {
			f.childRunLogger(&ChildRunEvent{Kind: ChildRunKindModelRouter, Stage: ChildRunStageModelRoute, Agent: agent.Name, Item: item})

			if f.diagnostics {
				emitSubagentDiagnostic(parentOutput, &SubagentDiagnostic{Name: routed.Name, Label: "model-router", Subagent: &SubagentDiagnostic{Name: agent.Name, Label: nestedDiagnosticLabel(item), Text: item.Text, Tool: item.Tool, Subagent: item.Subagent, Provider: item.Provider}})
			}

			if item.Kind == ChatResponseAssistantMessage {
				last = item.Text
			}
		}

		return nil
	})

	if err := child.Loop(ctx, input, func(func(SessionEntry, error) bool) {}, func(SessionEntry) error { return nil }, make(chan os.Signal, 1)); err != nil {
		_ = group.Wait()
		return ModelOption{}, fmt.Errorf("model router failed: %w", err)
	}

	if err := group.Wait(); err != nil {
		return ModelOption{}, fmt.Errorf("model router failed: %w", err)
	}

	var pick ModelOption
	if err := json.Unmarshal([]byte(last), &pick); err != nil {
		return ModelOption{}, errors.New("model router returned invalid JSON")
	}

	if !modelOptionAllowed(pick, routed.ModelOptions) {
		return ModelOption{}, errors.New("model router returned a choice that is not in modelOptions")
	}

	return pick, nil
}

func modelOptionAllowed(pick ModelOption, options []ModelOption) bool {
	for _, option := range options {
		if pick.Model == option.Model && pick.ReasoningEffort == option.ReasoningEffort && pick.Verbosity == option.Verbosity {
			return true
		}
	}

	return false
}

func modelRouterPrompt(message string, options []ModelOption) string {
	var builder strings.Builder

	builder.WriteString("Choose one allowed model option for the incoming message.\n\nIncoming message:\n")
	builder.WriteString(message)
	builder.WriteString("\n\nAllowed options:\n")

	for _, option := range options {
		builder.WriteString("- model: ")
		builder.WriteString(option.Model)
		builder.WriteString(" reasoningEffort: ")
		builder.WriteString(option.ReasoningEffort)
		builder.WriteString(" verbosity: ")
		builder.WriteString(option.Verbosity)
		builder.WriteByte('\n')
	}

	return builder.String()
}

func modelRouterResponseFormat() responses.ResponseFormatTextConfigUnionParam {
	var jsonSchema responses.ResponseFormatTextJSONSchemaConfigParam

	jsonSchema.Name = "model_router_choice"
	jsonSchema.Strict = openai.Bool(true)
	jsonSchema.Schema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"model":           map[string]any{"type": "string"},
			"reasoningEffort": map[string]any{"type": "string"},
			"verbosity":       map[string]any{"type": "string"},
		},
		"required":             []string{"model", "reasoningEffort", "verbosity"},
		"additionalProperties": false,
	}

	var responseFormat responses.ResponseFormatTextConfigUnionParam

	responseFormat.OfJSONSchema = &jsonSchema

	return responseFormat
}
