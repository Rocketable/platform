package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
)

type harnessFeedbackIssue struct {
	id                     int64
	summary, channelID, ts string
}

func harnessFeedbackTool(store *SessionService, threads *threadBridgeManager, resolver harness.ModelResolver, cfg *config.Config, agent, conversationID string) harness.Tool {
	return harness.Tool{Name: harness.HarnessFeedbackToolName, Description: "Report a problem with RocketClaw's harness itself that slowed you down or that you had to work around: a confusing or wrong tool result or error, a misleading description, a missing capability, a permission or sandbox surprise. Not for problems in the task, the user's code or outside services. People read these to fix the harness. summary: one line naming the problem. details: what you tried, what happened, what you expected and how you worked around it, with exact tool names and error text.", Permission: "rocketclaw", VisibilitySubjects: []string{harness.HarnessFeedbackToolName}, Subjects: func(json.RawMessage) ([]string, error) { return []string{harness.HarnessFeedbackToolName}, nil }, Parameters: map[string]any{"properties": map[string]any{"summary": map[string]any{"type": "string"}, "details": map[string]any{"type": "string"}}, "required": []string{"summary", "details"}}, Call: func(ctx context.Context, raw json.RawMessage, _ chan<- harness.ChatResponse) (harness.ToolResult, error) {
		var input struct {
			Summary string `json:"summary"`
			Details string `json:"details"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return harness.ToolResult{}, fmt.Errorf("parse harness feedback: %w", err)
		}

		summary := strings.Join(strings.Fields(input.Summary), " ")
		if summary == "" {
			return harness.ToolResult{}, errors.New("harness feedback needs a summary")
		}

		link, errLink := threads.WebURL(conversationID)
		if errLink != nil {
			link = "session " + conversationID
		}

		threads.mu.Lock()
		poster := threads.feedback
		threads.mu.Unlock()

		tx, err := store.beginStateTx(ctx, "harness feedback")
		if err != nil {
			return harness.ToolResult{}, err
		}
		defer func() { _ = tx.Rollback() }()

		if _, err := execState(ctx, tx, "lock harness feedback", `SELECT pg_advisory_xact_lock(hashtext('harness-feedback'))`); err != nil {
			return harness.ToolResult{}, err
		}

		issues, err := queryRows(ctx, tx, `SELECT id, summary, slack_channel_id, slack_ts FROM harness_feedback_issues WHERE reported_at > now() - interval '14 days' ORDER BY id`, "harness feedback issues", func(row rowScanner) (harnessFeedbackIssue, error) {
			var issue harnessFeedbackIssue
			return issue, row.Scan(&issue.id, &issue.summary, &issue.channelID, &issue.ts)
		})
		if err != nil {
			return harness.ToolResult{}, err
		}

		summaries := make([]string, len(issues))
		for i := range issues {
			summaries[i] = issues[i].summary
		}

		match, err := harness.MatchHarnessFeedback(ctx, resolver, cfg.TitleModel, summary+"\n\n"+input.Details, summaries)
		if err != nil {
			return harness.ToolResult{}, fmt.Errorf("match harness feedback: %w", err)
		}

		report := "Agent " + agent + " in " + link + "\n\n" + input.Details

		if match == 0 {
			channelID, ts, err := poster.PostHarnessFeedback(ctx, cfg.Slack.FeedbackChannel, "", "New harness issue: "+summary+"\n"+report)
			if err != nil {
				return harness.ToolResult{}, fmt.Errorf("post new harness issue: %w", err)
			}

			if _, err := execState(ctx, tx, "insert harness feedback issue", `INSERT INTO harness_feedback_issues (summary, slack_channel_id, slack_ts) VALUES ($1, $2, $3)`, summary, channelID, ts); err != nil {
				return harness.ToolResult{}, err
			}

			return harness.TextToolResult("Reported as a new harness issue."), commitState(tx, "harness feedback")
		}

		issue := issues[match-1]
		if _, _, err := poster.PostHarnessFeedback(ctx, issue.channelID, issue.ts, "Reported again: "+summary+"\n"+report); err != nil {
			return harness.ToolResult{}, fmt.Errorf("post repeated harness issue: %w", err)
		}

		if _, err := execState(ctx, tx, "touch harness feedback issue", `UPDATE harness_feedback_issues SET reported_at = now() WHERE id = $1`, issue.id); err != nil {
			return harness.ToolResult{}, err
		}

		return harness.TextToolResult("Added to the known harness issue: " + issue.summary + "."), commitState(tx, "harness feedback")
	}}
}
