package backend

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
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

	// Store-wide session reads compete with live turns for the State Store, so each call is capped.
	maxListedSessions = 200
	maxSessionEntries = 100
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
	Criteria              string `json:"criteria"`
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

func listSessionsTool(service *SessionService, threads *threadBridgeManager) rocketcode.Tool {
	return rocketcode.Tool{
		Name: listSessionsToolName, Description: fmt.Sprintf("List durable conversations of this State Store, newest first. Supply all five fields: criteria, since (required), until (empty for no upper bound), limit (1 to %d) and include_message_preview. Returns TSV with a header, one session per row with entry counts, timestamps and optional message previews. "+
			"Empty criteria lists every stored conversation by latest entry, including private MCP and cron sessions. "+
			"Other criteria is a Web /search query and returns what /search would: only the chats Web lists, never private External MCP or cron producer chats, filtered before the limit; since, until and order then use each chat's summary time. "+
			"Query language: tag:V, cron:V, agent:V and room:V match V exactly and case-sensitively, and V may be a JSON-quoted string such as tag:\"Needs review\"; is:pinned, is:forked and is:cron; sort:newest or sort:oldest. Unknown or malformed terms are searched as text. "+
			"All remaining text is ONE case-insensitive phrase, not separate keywords, matched against name, room, agent, preview, session label, chat origin and message text. "+
			"With criteria, rows add name, agent, room, tags and matched (the field holding the phrase, or messages when only message text did), and a messages block lists up to 3 matching messages per listed chat; rocketclaw_get_session with a row's before_entry_id and limit 1 shows that message. Messages of stopped turns are not listed. "+
			"Trailing lines show how criteria was read and whether the limit cut results or the message index or session summaries were incomplete.", maxListedSessions),
		Permission: "rocketclaw", VisibilitySubjects: []string{listSessionsToolName},
		Subjects: func(json.RawMessage) ([]string, error) { return []string{listSessionsToolName}, nil },
		Parameters: map[string]any{"properties": map[string]any{
			"criteria":                map[string]any{"type": "string", "description": "Web session search query, or empty for every stored conversation."},
			"since":                   map[string]any{"type": "string", "description": "Required inclusive bound on the latest entry, or on the summary time with criteria: Go duration relative to now or RFC3339Nano."},
			"until":                   map[string]any{"type": "string", "description": "Exclusive bound like since: RFC3339Nano, or empty for no bound."},
			"limit":                   map[string]any{"type": "integer", "minimum": 1, "maximum": maxListedSessions},
			"include_message_preview": map[string]any{"type": "boolean"},
		}, "required": []string{"criteria", "since", "until", "limit", "include_message_preview"}},
		Call: func(ctx context.Context, raw json.RawMessage, _ chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
			var params sessionListParams
			if err := json.Unmarshal(raw, &params); err != nil {
				return rocketcode.ToolResult{}, fmt.Errorf("parse list sessions: %w", err)
			}

			if params.Limit < 1 || params.Limit > maxListedSessions {
				return rocketcode.ToolResult{}, fmt.Errorf("limit must be between 1 and %d", maxListedSessions)
			}

			now := time.Now().UTC()

			params.Since, params.Until = strings.TrimSpace(params.Since), strings.TrimSpace(params.Until)
			if params.Since == "" {
				return rocketcode.ToolResult{}, errors.New("since is required")
			}

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

			var (
				ids     []string
				search  *SessionSearch
				matches []SessionMatch
				shown   = make(map[string]int) // Message rows printed per listed chat.
				err     error
			)

			if criteria := strings.TrimSpace(params.Criteria); criteria == "" {
				// Stored timestamps are UTC RFC3339Nano. Removing Z makes text order exact
				// even across whole/fractional seconds, without PostgreSQL's microsecond rounding.
				ids, err = queryStrings(ctx, service.db, `SELECT conversation_id FROM session_entries GROUP BY conversation_id
HAVING MAX(rtrim(entry_timestamp, 'Z') COLLATE "C") >= $1 COLLATE "C"
AND ($2 = '' OR MAX(rtrim(entry_timestamp, 'Z') COLLATE "C") < $2 COLLATE "C")
ORDER BY MAX(rtrim(entry_timestamp, 'Z') COLLATE "C") DESC, conversation_id COLLATE "C" LIMIT $3`, "session tool candidates", params.Since, params.Until, params.Limit)
			} else {
				threads.mu.Lock()
				slack := threads.slackLookups
				threads.mu.Unlock()

				search, matches, err = service.searchSessionsWithin(ctx, criteria, &params, slack)
				for i := range matches[:min(len(matches), params.Limit)] {
					ids = append(ids, matches[i].Chat.Session.Conversation.ID)
					shown[ids[i]] = 0
				}
			}

			if err != nil {
				return rocketcode.ToolResult{}, err
			}

			summaries, err := service.sessionToolSummaries(ctx, ids, params.IncludeMessagePreview)
			if err != nil {
				return rocketcode.ToolResult{}, err
			}

			var output strings.Builder

			escape := strings.NewReplacer("\\", "\\\\", "\t", "\\t", "\r", "\\r", "\n", "\\n")

			output.WriteString("conversation_id\tturns\tlast_updated")

			if search != nil {
				output.WriteString("\tname\tagent\troom\ttags\tmatched")
			}

			if params.IncludeMessagePreview {
				output.WriteString("\tlast_user_message\tlast_assistant_message")
			}

			output.WriteByte('\n')

			for i, summary := range summaries {
				var columns string

				if search != nil {
					chat := &matches[i].Chat

					matched := cmp.Or(string(matches[i].Field), "messages")
					if search.Needle == "" {
						matched = ""
					}

					summary.LastUpdated = chat.Session.Summary.LastUpdated.Format(time.RFC3339)
					columns = fmt.Sprintf("\t%s\t%s\t%s\t%s\t%s", escape.Replace(chat.Session.Name), escape.Replace(chat.Session.Conversation.Agent), escape.Replace(chat.Room), escape.Replace(strings.Join(chat.Session.Tags, ",")), matched)
				}

				fmt.Fprintf(&output, "%s\t%d\t%s%s", escape.Replace(summary.ConversationID), summary.Turns, summary.LastUpdated, columns)

				if params.IncludeMessagePreview {
					fmt.Fprintf(&output, "\t%s\t%s", escape.Replace(summary.LastUserMessage), escape.Replace(summary.LastAssistantMessage))
				}

				output.WriteByte('\n')
			}

			if search != nil {
				var hits strings.Builder

				for _, hit := range search.Hits {
					// A stopped turn's hit has no message ID, so rocketclaw_get_session cannot open it.
					if count, listed := shown[hit.ConversationID]; !listed || count == 3 || hit.MessageID == "" {
						continue
					}

					shown[hit.ConversationID]++
					entry, _, _ := strings.Cut(hit.MessageID, ":")
					id, _ := strconv.ParseInt(entry, 10, 64) // The store builds message IDs from entry IDs.

					text := []rune(hit.Text)
					fmt.Fprintf(&hits, "%s\t%s\t%d\t%s\t%s\n", escape.Replace(hit.ConversationID), hit.MessageID, id+1, hit.Role, escape.Replace(string(text[:min(len(text), 300)])))
				}

				if hits.Len() > 0 {
					output.WriteString("\nmessages\nconversation_id\tmessage_id\tbefore_entry_id\trole\ttext\n" + hits.String())
				}

				terms := make([]string, 0, len(search.Terms))
				for _, term := range search.Terms {
					terms = append(terms, term.Text)
				}

				fmt.Fprintf(&output, "[criteria: terms=%s; text=%s]\n", escape.Replace(strings.Join(terms, ",")), escape.Replace(search.Text))

				if more := len(matches) - len(ids); more > 0 {
					fmt.Fprintf(&output, "[truncated: %d more matching sessions]\n", more)
				}

				if !search.IndexComplete {
					output.WriteString("[message index incomplete]\n")
				}

				if !search.SummariesComplete {
					output.WriteString("[session summaries incomplete]\n")
				}
			}

			return rocketcode.TextToolResult(output.String()), nil
		},
	}
}

// searchSessionsWithin runs SearchSessions for criteria with message search and
// keeps the matches whose summary time is within the normalized since and until
// bounds, newest first unless criteria has a sort: term.
func (s *SessionService) searchSessionsWithin(ctx context.Context, criteria string, params *sessionListParams, slack slackLookup) (*SessionSearch, []SessionMatch, error) {
	search, err := s.SearchSessions(ctx, criteria, true, slack)
	if err != nil {
		return nil, nil, err
	}

	var matches []SessionMatch

	for i := range search.Matches {
		// Summary times compare as the bounds do: UTC RFC3339Nano text without Z.
		if summary := search.Matches[i].Chat.Session.Summary; summary != nil {
			if at := strings.TrimSuffix(summary.LastUpdated.Format(time.RFC3339Nano), "Z"); at >= params.Since && (params.Until == "" || at < params.Until) {
				matches = append(matches, search.Matches[i])
			}
		}
	}

	if !slices.ContainsFunc(search.Terms, func(term SearchTerm) bool { return term.Key == keySort }) {
		slices.SortFunc(matches, func(a, b SessionMatch) int {
			return cmp.Or(b.Chat.Session.Summary.LastUpdated.Compare(a.Chat.Session.Summary.LastUpdated), strings.Compare(a.Chat.Session.Conversation.ID, b.Chat.Session.Conversation.ID))
		})
	}

	return search, matches, nil
}

func (s *SessionService) sessionToolSummaries(ctx context.Context, ids []string, preview bool) ([]sessionListSummary, error) {
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

			if !preview {
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
		Name: getSessionToolName, Description: fmt.Sprintf("Read the newest limit (1 to %d) stored entries of a durable conversation older than before_entry_id (0 for the newest) as timestamp, role and content TSV in stored entry order, including messages, readable reasoning and tool events before compaction. When older entries exist, the last line is [next_before_entry_id=N]; pass N to read the previous page. Store-wide access includes private MCP sessions. One snapshot, no polling or read-state changes. Use rocketclaw_current_session_id for your owning conversation or rocketclaw_list_sessions to find another ID.", maxSessionEntries),
		Permission: "rocketclaw", VisibilitySubjects: []string{getSessionToolName},
		Subjects: func(json.RawMessage) ([]string, error) { return []string{getSessionToolName}, nil },
		Parameters: map[string]any{"properties": map[string]any{
			"conversation_id": map[string]any{"type": "string"},
			"limit":           map[string]any{"type": "integer", "minimum": 1, "maximum": maxSessionEntries},
			"before_entry_id": map[string]any{"type": "integer", "minimum": 0},
		}, "required": []string{"conversation_id", "limit", "before_entry_id"}},
		Call: func(ctx context.Context, raw json.RawMessage, _ chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
			var params struct {
				ConversationID string `json:"conversation_id"`
				Limit          int    `json:"limit"`
				BeforeEntryID  int64  `json:"before_entry_id"`
			}
			if err := json.Unmarshal(raw, &params); err != nil {
				return rocketcode.ToolResult{}, fmt.Errorf("parse get session: %w", err)
			}

			params.ConversationID = strings.TrimSpace(params.ConversationID)
			if params.ConversationID == "" {
				return rocketcode.ToolResult{}, errors.New("conversation ID is required")
			}

			if params.Limit < 1 || params.Limit > maxSessionEntries || params.BeforeEntryID < 0 {
				return rocketcode.ToolResult{}, fmt.Errorf("limit must be between 1 and %d and before_entry_id must not be negative", maxSessionEntries)
			}

			// The newest limit+1 rows are read, so one extra row reveals whether an older page exists.
			observations, err := queryRows(ctx, service.db, `WITH `+sessionHistorySQL+`
SELECT id, entry_json, source_conversation_id, synced, revert_index FROM effective_entries
WHERE $2::bigint = 0 OR id < $2 ORDER BY id DESC LIMIT $3`, "session tool entries", scanObservedEntry, params.ConversationID, params.BeforeEntryID, params.Limit+1)
			if err != nil {
				return rocketcode.ToolResult{}, err
			}

			older := len(observations) > params.Limit
			observations = observations[:min(len(observations), params.Limit)]
			slices.Reverse(observations)

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

			if older {
				fmt.Fprintf(&output, "[next_before_entry_id=%d]\n", observations[0].ID)
			}

			return rocketcode.TextToolResult(output.String()), nil
		},
	}
}
