package rocketcode

import "encoding/json"

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
	ReplayInput              []json.RawMessage          `json:"replay_input,omitempty"`
	OutputTrace              []json.RawMessage          `json:"output_trace,omitempty"`
	TokenUsage               *TokenUsage                `json:"token_usage,omitempty"`
	ResponseID               string                     `json:"response_id,omitempty"`
	OpenFunctionCalls        []FunctionCallCheckpoint   `json:"open_function_calls,omitempty"`
	CompletedFunctionOutputs []FunctionOutputCheckpoint `json:"completed_function_outputs,omitempty"`
}
