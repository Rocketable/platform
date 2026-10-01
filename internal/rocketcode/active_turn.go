package rocketcode

import (
	"encoding/json"
	"slices"
)

// ReplayAttribution overrides a recovered half-open replay range, including unknown values.
type ReplayAttribution struct {
	Start           int     `json:"start"`
	End             int     `json:"end"`
	Agent           string  `json:"agent,omitempty"`
	Model           string  `json:"model,omitempty"`
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
}

// RemapReplayAttribution moves overrides to projected replay boundaries in place.
func RemapReplayAttribution(ranges []ReplayAttribution, boundaries []int) {
	for i := range ranges {
		ranges[i].Start = boundaries[ranges[i].Start]
		ranges[i].End = boundaries[ranges[i].End]
	}
}

// AttributionAt returns the execution that produced a replay item.
func (e *SessionEntry) AttributionAt(index int) ReplayAttribution {
	for _, attribution := range e.ReplayAttribution {
		if index >= attribution.Start && index < attribution.End {
			return attribution
		}
	}

	return ReplayAttribution{Agent: e.Agent, Model: e.Model, ReasoningEffort: e.ReasoningEffort}
}

func (e *SessionEntry) attributionRanges(offset int) []ReplayAttribution {
	ranges := slices.Clone(e.ReplayAttribution)

	ranges = append(ranges, ReplayAttribution{End: len(e.ReplayInput), Agent: e.Agent, Model: e.Model, ReasoningEffort: e.ReasoningEffort})
	for i := range ranges {
		ranges[i].Start += offset
		ranges[i].End += offset
	}

	return ranges
}

// FunctionCallCheckpoint contains replay-safe data for a model-emitted function call.
type FunctionCallCheckpoint struct {
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// FunctionOutputCheckpoint contains replay-safe data for a completed function-call output.
type FunctionOutputCheckpoint struct {
	CallID      string            `json:"call_id"`
	Name        string            `json:"name,omitempty"`
	ReplayInput []json.RawMessage `json:"replay_input,omitempty"`
}

// ActiveTurnCheckpoint is RocketCode's durable, embedder-neutral checkpoint for one root turn.
type ActiveTurnCheckpoint struct {
	TurnID                   string                     `json:"turn_id"`
	ConversationKey          string                     `json:"conversation_key"`
	Agent                    string                     `json:"agent"`
	Model                    string                     `json:"model"`
	DisplayModel             string                     `json:"display_model"`
	ReasoningEffort          *string                    `json:"reasoning_effort,omitempty"`
	ReplayAttribution        []ReplayAttribution        `json:"replay_attribution,omitempty"`
	ReplayInput              []json.RawMessage          `json:"replay_input,omitempty"`
	ReplayInputIDs           map[string]int             `json:"replay_input_ids,omitempty"`
	OutputTrace              []json.RawMessage          `json:"output_trace,omitempty"`
	TokenUsage               *TokenUsage                `json:"token_usage,omitempty"`
	ResponseID               string                     `json:"response_id,omitempty"`
	OpenFunctionCalls        []FunctionCallCheckpoint   `json:"open_function_calls,omitempty"`
	CompletedFunctionOutputs []FunctionOutputCheckpoint `json:"completed_function_outputs,omitempty"`
}
