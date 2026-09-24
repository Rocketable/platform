package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketcode"
)

const (
	listSessionsToolName     = "rocketclaw_list_sessions"
	getSessionToolName       = "rocketclaw_get_session"
	currentSessionIDToolName = "rocketclaw_current_session_id"
)

type sessionListParams struct {
	Since                 string `json:"since"`
	Until                 string `json:"until"`
	Limit                 int    `json:"limit"`
	IncludeMessagePreview bool   `json:"include_message_preview"`
}

type sessionListSummary struct {
	ConversationID       string
	Turns                int
	LastUpdated          string
	LastUserMessage      string
	LastAssistantMessage string
}

func listSessionsTool(service *SessionService) rocketcode.Tool {
	return rocketcode.Tool{
		Name: listSessionsToolName, Description: "List durable conversations across this State Store, including private MCP and cron sessions. Supply all four fields: use empty since/until strings for no time bounds, limit 0 for unlimited results, and include_message_preview true or false. Prefer bounded searches. Returns TSV with a header, one session per row, entry counts, timestamps and optional message previews.",
		Permission: "rocketclaw", VisibilitySubjects: []string{listSessionsToolName},
		Subjects: func(json.RawMessage) ([]string, error) { return []string{listSessionsToolName}, nil },
		Parameters: map[string]any{"properties": map[string]any{
			"since":                   map[string]any{"type": "string", "description": "Inclusive latest-entry bound: Go duration relative to now, RFC3339Nano, or empty for no bound."},
			"until":                   map[string]any{"type": "string", "description": "Exclusive latest-entry bound: RFC3339Nano, or empty for no bound."},
			"limit":                   map[string]any{"type": "integer", "minimum": 0},
			"include_message_preview": map[string]any{"type": "boolean"},
		}, "required": []string{"since", "until", "limit", "include_message_preview"}},
		Call: func(ctx context.Context, raw json.RawMessage, _ chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
			var params sessionListParams
			if err := json.Unmarshal(raw, &params); err != nil {
				return rocketcode.ToolResult{}, fmt.Errorf("parse list sessions: %w", err)
			}

			if params.Limit < 0 {
				return rocketcode.ToolResult{}, errors.New("limit must not be negative")
			}

			now := time.Now().UTC()

			params.Since, params.Until = strings.TrimSpace(params.Since), strings.TrimSpace(params.Until)
			for _, bound := range []*string{&params.Since, &params.Until} {
				if *bound == "" {
					continue
				}

				var at time.Time
				if duration, err := time.ParseDuration(*bound); bound == &params.Since && err == nil {
					at = now.Add(-duration)
				} else {
					parsed, err := time.Parse(time.RFC3339Nano, *bound)
					if err != nil {
						return rocketcode.ToolResult{}, fmt.Errorf("parse session time bound: %w", err)
					}

					at = parsed
				}

				*bound = strings.TrimSuffix(at.UTC().Format(time.RFC3339Nano), "Z")
			}

			summaries, err := service.sessionToolSummaries(ctx, params)
			if err != nil {
				return rocketcode.ToolResult{}, err
			}

			var output strings.Builder

			escape := strings.NewReplacer("\\", "\\\\", "\t", "\\t", "\r", "\\r", "\n", "\\n")

			output.WriteString("conversation_id\tturns\tlast_updated")

			if params.IncludeMessagePreview {
				output.WriteString("\tlast_user_message\tlast_assistant_message")
			}

			output.WriteByte('\n')

			for _, summary := range summaries {
				fmt.Fprintf(&output, "%s\t%d\t%s", escape.Replace(summary.ConversationID), summary.Turns, summary.LastUpdated)

				if params.IncludeMessagePreview {
					fmt.Fprintf(&output, "\t%s\t%s", escape.Replace(summary.LastUserMessage), escape.Replace(summary.LastAssistantMessage))
				}

				output.WriteByte('\n')
			}

			return rocketcode.TextToolResult(output.String()), nil
		},
	}
}

func (s *SessionService) sessionToolSummaries(ctx context.Context, params sessionListParams) ([]sessionListSummary, error) {
	// Stored timestamps are UTC RFC3339Nano. Removing Z makes text order exact
	// even across whole/fractional seconds, without PostgreSQL's microsecond rounding.
	query := `SELECT conversation_id FROM session_entries GROUP BY conversation_id
HAVING ($1 = '' OR MAX(rtrim(entry_timestamp, 'Z') COLLATE "C") >= $1 COLLATE "C")
AND ($2 = '' OR MAX(rtrim(entry_timestamp, 'Z') COLLATE "C") < $2 COLLATE "C") ORDER BY `
	if params.Since != "" || params.Until != "" || params.Limit > 0 {
		query += `MAX(rtrim(entry_timestamp, 'Z') COLLATE "C") DESC, `
	}

	query += `conversation_id COLLATE "C" LIMIT NULLIF($3, 0)`

	ids, err := queryStrings(ctx, s.db, query, "session tool candidates", params.Since, params.Until, params.Limit)
	if err != nil {
		return nil, err
	}
	// Selected conversations still require a full-history scan. A dedicated summary
	// projection is the upgrade path if measured inspection cost warrants it.
	summaries := make([]sessionListSummary, 0, len(ids))
	for _, id := range ids {
		entries, err := queryRows(ctx, s.db, `SELECT entry_json FROM session_entries WHERE conversation_id = $1 ORDER BY id`, "session tool entries", func(row rowScanner) (rocketcode.SessionEntry, error) {
			var (
				raw   string
				entry rocketcode.SessionEntry
			)
			if err := row.Scan(&raw); err != nil {
				return entry, fmt.Errorf("scan session tool entry: %w", err)
			}

			if err := json.Unmarshal([]byte(raw), &entry); err != nil {
				return entry, fmt.Errorf("decode session tool entry: %w", err)
			}

			return entry, nil
		}, id)
		if err != nil {
			return nil, err
		}

		summary := sessionListSummary{ConversationID: id, Turns: len(entries)}
		for i := range entries {
			entry := &entries[i]
			summary.LastUpdated = entry.Timestamp.Format(time.RFC3339)

			if !params.IncludeMessagePreview {
				continue
			}

			messages, err := replayInputMessages(entry.ReplayInput)
			if err != nil {
				return nil, err
			}

			for _, message := range messages {
				switch message.role {
				case "user":
					summary.LastUserMessage = message.text
				case "assistant":
					summary.LastAssistantMessage = message.text
				}
			}
		}

		summaries = append(summaries, summary)
	}

	return summaries, nil
}

func currentSessionIDTool(conversationID string) rocketcode.Tool {
	return rocketcode.Tool{
		Name: currentSessionIDToolName, Description: "Return only the owning RocketClaw bridge's bare stored conversation ID, without a JSON wrapper. Pass it as conversation_id to rocketclaw_get_session to read durable history, including entries before compaction. Child agents inherit this owning conversation ID, not a separate child-run ID. No arguments.",
		Permission: "rocketclaw", VisibilitySubjects: []string{currentSessionIDToolName},
		Subjects:   func(json.RawMessage) ([]string, error) { return []string{currentSessionIDToolName}, nil },
		Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false},
		Call: func(context.Context, json.RawMessage, chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
			return rocketcode.TextToolResult(conversationID), nil
		},
	}
}

func getSessionTool(service *SessionService) rocketcode.Tool {
	return rocketcode.Tool{
		Name: getSessionToolName, Description: "Read a durable conversation as timestamp, role and content TSV in stored entry order, including messages, readable reasoning and tool events before compaction. Store-wide access includes private MCP sessions. One snapshot, no polling or read-state changes. Histories can be large; use rocketclaw_current_session_id for your owning conversation or rocketclaw_list_sessions to find another ID.",
		Permission: "rocketclaw", VisibilitySubjects: []string{getSessionToolName},
		Subjects:   func(json.RawMessage) ([]string, error) { return []string{getSessionToolName}, nil },
		Parameters: map[string]any{"properties": map[string]any{"conversation_id": map[string]any{"type": "string"}}, "required": []string{"conversation_id"}},
		Call: func(ctx context.Context, raw json.RawMessage, _ chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
			var params struct {
				ConversationID string `json:"conversation_id"`
			}
			if err := json.Unmarshal(raw, &params); err != nil {
				return rocketcode.ToolResult{}, fmt.Errorf("parse get session: %w", err)
			}

			observations, err := service.ObserveEntries(ctx, params.ConversationID)
			if err != nil {
				return rocketcode.ToolResult{}, err
			}

			var output strings.Builder

			escape := strings.NewReplacer("\\", "\\\\", "\t", "\\t", "\r", "\\r", "\n", "\\n")

			output.WriteString("timestamp\trole\tcontent\n")

			for i := range observations {
				entry := &observations[i].Entry
				timestamp := entry.Timestamp.Format(time.RFC3339Nano)
				// Trace-only events have no recorded interleaving with replay items.
				for _, source := range [][]json.RawMessage{entry.ReplayInput, entry.OutputTrace} {
					for _, raw := range source {
						kind := replayInputRawKind(raw)
						switch kind {
						case "", "message", "function_call", "function_call_output", "reasoning", "compaction":
						default:
							fmt.Fprintf(&output, "%s\tevent\t[%s: stored event not rendered]\n", timestamp, escape.Replace(kind))
							continue
						}

						items, err := rocketcode.ReplayInputToParams([]json.RawMessage{raw})
						if err != nil {
							return rocketcode.ToolResult{}, fmt.Errorf("decode session event: %w", err)
						}

						item := &items[0]

						role, text, message, err := ReplayInputMessageRoleText(item, raw)
						if err != nil {
							return rocketcode.ToolResult{}, err
						}

						switch {
						case message:
						case item.OfFunctionCall != nil:
							call := item.OfFunctionCall
							role, text = "tool_call", call.Name+" ["+call.CallID+"] "+call.Arguments
						case item.OfFunctionCallOutput != nil:
							result := item.OfFunctionCallOutput

							parts := make([]string, 0, len(result.Output.OfResponseFunctionCallOutputItemArray))
							for _, part := range result.Output.OfResponseFunctionCallOutputItemArray {
								if part.OfInputText != nil {
									parts = append(parts, part.OfInputText.Text)
								} else {
									parts = append(parts, "[non-text tool result omitted]")
								}
							}

							role, text = "tool_result", "["+result.CallID.Value+"] "+result.Output.OfString.Value+strings.Join(parts, "\n")
						case item.OfReasoning != nil:
							role = "reasoning"

							parts := make([]string, 0, len(item.OfReasoning.Summary)+len(item.OfReasoning.Content))
							for _, part := range item.OfReasoning.Summary {
								parts = append(parts, part.Text)
							}

							for _, part := range item.OfReasoning.Content {
								parts = append(parts, part.Text)
							}

							text = strings.Join(parts, "\n")
						case item.OfCompaction != nil:
							role, text = "compaction", "Compaction boundary"
						default:
							role, text = "event", "["+kind+": stored event not rendered]"
						}

						fmt.Fprintf(&output, "%s\t%s\t%s\n", timestamp, escape.Replace(role), escape.Replace(text))
					}
				}
			}

			return rocketcode.TextToolResult(output.String()), nil
		},
	}
}
