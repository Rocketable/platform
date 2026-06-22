package rocketcode

import (
	"cmp"
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"golang.org/x/sync/errgroup"
)

func (f *toolFactory) reviewPermission(ctx context.Context, request *permissionReviewRequest) permissionReviewDecision {
	if f.inPermissionReview {
		return permissionReviewDecision{Approved: false, Reason: "automatic permission review cannot recursively require automatic approval"}
	}

	agent := embeddedGuardianAgent()
	agent.Model = f.modelRef.display()

	if !request.ReviewerEmbedded {
		customAgent, ok := f.agents.Items[request.Reviewer]
		if !ok {
			return permissionReviewDecision{Approved: false, Reason: "automatic permission reviewer is missing"}
		}

		agent = customAgent
	}

	agent.Permission = f.shellOutput.effectivePermissions(agent.Permission)
	expandAgentPrompt(ctx, &agent, f.expandPromptShellCommands.SubagentPrompts, &f.promptExpansion)

	modelRef, err := parseAgentModelRef(agent.Model)
	if err != nil {
		return permissionReviewDecision{Approved: false, Reason: "automatic permission reviewer model failed: " + err.Error()}
	}

	client, err := f.responsesAPIForModel(modelRef)
	if err != nil {
		return permissionReviewDecision{Approved: false, Reason: "automatic permission reviewer model failed: " + err.Error()}
	}

	childFactory := *f
	childFactory.inPermissionReview = true
	childFactory.modelRef = modelRef
	childFactory.client = client.client

	child := &looper{
		agent:                  agent,
		provider:               modelRef.provider,
		modelRef:               modelRef,
		Client:                 client.client,
		AnthropicClient:        f.anthropicClient,
		SystemPrompt:           composeSystemPromptWithSkills(agent.Prompt, f.skills, &agent),
		Model:                  modelRef.apiModel,
		DisplayModel:           modelRef.display(),
		ReasoningEffort:        shared.ReasoningEffort(cmp.Or(agent.ReasoningEffort, string(f.reasoningEffort))),
		Verbosity:              agent.Verbosity,
		CompactThreshold:       f.compactThreshold,
		CompactionSteering:     f.compactionSteering,
		ParallelToolCalls:      f.parallelToolCalls,
		ResponseFormat:         permissionReviewResponseFormat(),
		Permissions:            agent.Permission,
		Tools:                  childFactory.toolsFor(&agent),
		AutoApprovePermissions: f.autoApprovePermissions,
		PermissionReviewer:     &childFactory,
		InPermissionReview:     true,
		Observability:          f.observability,
	}

	payload, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return permissionReviewDecision{Approved: false, Reason: "automatic permission review request failed: " + err.Error()}
	}

	output := make(chan ChatResponse)

	input := make(chan PromptInput, 1)
	input <- PromptInput{Role: PromptInputRoleUser, Text: "Review this planned tool call:\n```json\n" + string(payload) + "\n```", Responses: output}

	close(input)

	var (
		group errgroup.Group
		last  string
	)

	group.Go(func() error {
		for item := range output {
			if item.Kind == ChatResponseAssistantMessage {
				last = item.Text
			}
		}

		return nil
	})

	if err := child.Loop(ctx, input, func(func(SessionEntry, error) bool) {}, func(SessionEntry) error { return nil }, make(chan os.Signal, 1)); err != nil {
		_ = group.Wait()
		return permissionReviewDecision{Approved: false, Reason: "automatic permission review failed: " + err.Error()}
	}

	if err := group.Wait(); err != nil {
		return permissionReviewDecision{Approved: false, Reason: "automatic permission review failed: " + err.Error()}
	}

	var decision permissionReviewDecision
	if err := json.Unmarshal([]byte(last), &decision); err != nil {
		return permissionReviewDecision{Approved: false, Reason: "automatic permission reviewer returned invalid JSON"}
	}

	if !slices.Contains([]string{"low", "medium", "high", "critical"}, decision.Risk) {
		return permissionReviewDecision{Approved: false, Reason: "automatic permission reviewer returned invalid risk"}
	}

	if !slices.Contains([]string{"unknown", "low", "medium", "high"}, decision.Authorization) {
		return permissionReviewDecision{Approved: false, Reason: "automatic permission reviewer returned invalid authorization"}
	}

	if strings.TrimSpace(decision.Reason) == "" {
		return permissionReviewDecision{Approved: false, Reason: "automatic permission reviewer returned empty reason"}
	}

	return decision
}

func permissionReviewResponseFormat() responses.ResponseFormatTextConfigUnionParam {
	var jsonSchema responses.ResponseFormatTextJSONSchemaConfigParam

	jsonSchema.Name = "permission_review_decision"
	jsonSchema.Strict = openai.Bool(true)
	jsonSchema.Schema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"approved":      map[string]any{"type": "boolean"},
			"risk":          map[string]any{"type": "string", "enum": []string{"low", "medium", "high", "critical"}},
			"authorization": map[string]any{"type": "string", "enum": []string{"unknown", "low", "medium", "high"}},
			"reason":        map[string]any{"type": "string"},
		},
		"required":             []string{"approved", "risk", "authorization", "reason"},
		"additionalProperties": false,
	}

	var responseFormat responses.ResponseFormatTextConfigUnionParam

	responseFormat.OfJSONSchema = &jsonSchema

	return responseFormat
}
