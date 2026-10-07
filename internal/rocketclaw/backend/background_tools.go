package backend

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Rocketable/platform/internal/rocketcode"
)

const stopBackgroundJobToolName = "rocketclaw_stop_background_job"

// stopBackgroundJobTool stops a Background Job listed at conversationID: one of its own or its
// subagents', or one of a hidden run that reports there. Like background mode itself,
// it is visible and callable only for agents with the exact allow_background rule.
func stopBackgroundJobTool(registry *backgroundRegistry, conversationID string) rocketcode.Tool {
	return rocketcode.Tool{Name: stopBackgroundJobToolName, Description: "Stop a running Background Job of this conversation by its job ID, including jobs of your subagents and of hidden runs that report here.", Permission: "rocketclaw", VisibilitySubjects: []string{rocketcode.AllowBackgroundSubject}, Subjects: func(json.RawMessage) ([]string, error) { return []string{rocketcode.AllowBackgroundSubject}, nil }, Parameters: map[string]any{"properties": map[string]any{"job_id": map[string]any{"type": "string", "description": "ID of the Background Job to stop."}}}, Call: func(ctx context.Context, raw json.RawMessage, _ chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
		var input struct {
			JobID string `json:"job_id"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return rocketcode.ToolResult{}, fmt.Errorf("parse stop background job: %w", err)
		}

		stopped, err := registry.stop(ctx, conversationID, input.JobID, errStoppedByAgent)
		if err != nil {
			return rocketcode.ToolResult{}, err
		}

		if !stopped {
			return rocketcode.TextToolResult("Background job " + input.JobID + " had already finished."), nil
		}

		return rocketcode.TextToolResult("Stopped background job " + input.JobID + "."), nil
	}}
}
