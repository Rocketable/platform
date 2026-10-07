package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketcode"
)

const (
	listSessionsToolName     = "rocketclaw_list_sessions"
	getSessionToolName       = "rocketclaw_get_session"
	currentSessionIDToolName = "rocketclaw_current_session_id"
	setTagToolName           = "rocketclaw_set_tag"
	getTagsToolName          = "rocketclaw_get_tags"
)

func agentTagGroups(agent *rocketcode.Agent) ([][]string, error) {
	permission, _ := agent.Frontmatter["permission"].(map[string]any)
	rocketclaw, _ := permission["rocketclaw"].(map[string]any)

	raw, present := rocketclaw[setTagToolName]
	if !present {
		return nil, nil
	}

	if _, scalar := raw.(string); scalar {
		return nil, nil
	}

	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode tag permissions: %w", err)
	}

	var groups [][]string
	if err := json.Unmarshal(data, &groups); err != nil {
		return nil, fmt.Errorf("decode tag groups: %w", err)
	}

	if groups == nil {
		return nil, errors.New("tags must be a list of tag lists")
	}

	var names []string

	for _, group := range groups {
		if len(group) == 0 {
			return nil, errors.New("tag groups must not be empty")
		}

		for _, name := range group {
			if name == "" || slices.Contains(names, name) {
				return nil, fmt.Errorf("empty or duplicate tag name %q", name)
			}

			names = append(names, name)
		}
	}

	return groups, nil
}

func appendSessionTagPrompt(agent *rocketcode.Agent, groups [][]string) {
	var tools []string

	for _, name := range []string{setTagToolName, getTagsToolName} {
		if action, _ := agent.Permission.Evaluate("rocketclaw_tags", name); action == rocketcode.PermissionAllow {
			tools = append(tools, name)
		}
	}

	if len(tools) == 0 {
		return
	}

	agent.Prompt += "\n\n## Session Tags\n\nAvailable tools: " + strings.Join(tools, ", ") + ". Tags belong to the owning session and persist across restarts. Allowed literal, case-sensitive groups: " + fmt.Sprintf("%q", groups) + ". Each group is exclusive: set an inactive tag to replace its group; set an active tag to toggle it off. Other groups stay unchanged. Get returns all active session tags."
}

func sessionTagTools(service *SessionService, conversationID string) []rocketcode.Tool {
	tools := make([]rocketcode.Tool, 0, 2)

	for _, name := range []string{setTagToolName, getTagsToolName} {
		parameters := map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false}
		description := "Return all durable active tags of the owning session as {\"tags\":[...]}. No arguments."

		if name == setTagToolName {
			parameters["properties"] = map[string]any{"tag": map[string]any{"type": "string"}}
			parameters["required"] = []string{"tag"}
			description = "Toggle a permitted literal tag on the owning session. An inactive tag replaces its exclusive group; an active tag is removed. Other groups stay unchanged. Return {\"tags\":[...]} in lexical order."
		}

		tools = append(tools, rocketcode.Tool{Name: name, Description: description, Permission: "rocketclaw_tags", VisibilitySubjects: []string{name}, Subjects: func(json.RawMessage) ([]string, error) { return []string{name}, nil }, Parameters: parameters,
			Call: func(ctx context.Context, raw json.RawMessage, _ chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
				agent, ok := rocketcode.ToolCallAgent(ctx)
				if !ok {
					return rocketcode.ToolResult{}, errors.New("tag tools require an active agent call")
				}

				groups, err := agentTagGroups(&agent)
				if err != nil {
					return rocketcode.ToolResult{}, err
				}

				var (
					params *struct {
						Tag *string `json:"tag"`
					}
					empty *struct{}
					input any = &params
				)
				if name == getTagsToolName {
					input = &empty
				}

				decoder := json.NewDecoder(bytes.NewReader(raw))
				decoder.DisallowUnknownFields()

				if err := decoder.Decode(input); err != nil {
					return rocketcode.ToolResult{}, fmt.Errorf("parse tag arguments: %w", err)
				}

				if !json.Valid(raw) || name == getTagsToolName && empty == nil || name == setTagToolName && (params == nil || params.Tag == nil) {
					return rocketcode.ToolResult{}, errors.New("invalid tag arguments")
				}

				var tags []string

				if name == setTagToolName {
					group := slices.IndexFunc(groups, func(group []string) bool { return slices.Contains(group, *params.Tag) })
					if group < 0 {
						return rocketcode.ToolResult{}, fmt.Errorf("tag %q is not permitted for agent %q", *params.Tag, agent.Name)
					}

					tags, err = service.toggleSessionTag(ctx, conversationID, *params.Tag, groups[group])
				} else {
					tags, err = sessionTags(ctx, service.db, conversationID)
				}

				if err != nil {
					return rocketcode.ToolResult{}, err
				}

				result, _ := json.Marshal(struct {
					Tags []string `json:"tags"`
				}{tags}) // Encoding a string slice cannot fail.

				return rocketcode.TextToolResult(string(result)), nil
			}})
	}

	return tools
}

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
		Name: currentSessionIDToolName, Description: "Return only the owning RocketClaw bridge's bare stored conversation ID, without a JSON wrapper. Pass it as conversation_id to rocketclaw_get_session inside execute to read durable history, including entries before compaction. Child agents inherit this owning conversation ID, not a separate child-run ID. No arguments.",
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
				for s, source := range [][]json.RawMessage{entry.ReplayInput, entry.OutputTrace} {
					for j, raw := range source {
						kind := replayInputRawKind(raw)
						if kind == "" && s == 1 {
							// Only replay input holds untyped messages; untyped trace data is entry bookkeeping such as a producer schedule.
							kind = entry.Type
						}

						switch kind {
						case "", "message", "function_call", "function_call_output", "reasoning", "compaction":
						default:
							fmt.Fprintf(&output, "%s\tevent\t[%s: stored event not rendered]\n", timestamp, escape.Replace(kind))
							continue
						}

						var (
							role, text string
							message    bool
						)

						items, err := rocketcode.ReplayInputToParams([]json.RawMessage{raw})
						if err == nil {
							role, text, message, err = ReplayInputMessageRoleText(&items[0], raw)
						}

						if err != nil {
							// The decoder only sees this one item, so its own location is a placeholder.
							if errReplay, ok := errors.AsType[*rocketcode.ReplayDecodeError](err); ok {
								err = errReplay.Cause
							}

							fmt.Fprintf(&output, "%s\tevent\t%s\n", timestamp, escape.Replace(fmt.Sprintf("[entry %d %s item %d: stored event not decoded: %v]", observations[i].ID, [...]string{"replay_input", "output_trace"}[s], j, err)))

							continue
						}

						item := &items[0]

						switch {
						case message:
							// The prompt header names the sender, so readers can tell an operator from a customer.
							if item.OfMessage != nil && (role == "user" || role == "developer") {
								if header, _ := item.OfMessage.ExtraFields()["prompt_header"].(string); header != "" {
									text = header + "\n\n" + text
								}
							}
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
