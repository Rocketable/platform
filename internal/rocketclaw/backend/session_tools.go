package backend

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
)

const (
	sessionsListToolName      = "rocketclaw_sessions_list"
	sessionsGetToolName       = "rocketclaw_sessions_get"
	sessionsCurrentIDToolName = "rocketclaw_sessions_current_id"
	setTagToolName            = "rocketclaw_set_tag"
	getTagsToolName           = "rocketclaw_get_tags"
	sessionsWriteToolName     = "rocketclaw_sessions_write"
	sessionsGetURLToolName    = "rocketclaw_sessions_get_url"

	// Store-wide session reads compete with live turns for the State Store, so each call is capped.
	maxListedSessions = 200
	maxSessionEntries = 100
)

// topLevelSessionCreator returns who created id when id is a top-level session: a chat Web shows.
func (s *SessionService) topLevelSessionCreator(ctx context.Context, id string) (string, error) {
	var spawnedBy string

	err := queryScalar(ctx, s.db, fmt.Sprintf("read session %q", id), `WITH `+visibleChatsSQL+` SELECT spawned_by FROM visible_chats WHERE conversation_id = $1`, &spawnedBy, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("session %q is not a top-level session", id)
	}

	return spawnedBy, err
}

// startSubagent records the subagent session conversationID of parent, run by agent, unless recorded. A
// continue, unlike a new subagent, needs a session saved before: recorded, or a Delegation History older
// than subagent sessions.
func (s *SessionService) startSubagent(ctx context.Context, conversationID, parent, agent string, isNew bool) error {
	started, err := execRows(ctx, s.db, "start subagent session", "count started subagent session", `WITH saved AS (
    SELECT count(*) AS entries FROM session_entries WHERE conversation_id = $1
)
INSERT INTO managed_conversations (conversation_id, agent, created_by, parent_conversation_id) SELECT $1, $3, '', $2 FROM saved WHERE $4 OR entries > 0
ON CONFLICT (conversation_id) DO NOTHING`, conversationID, parent, agent, isNew)
	if err != nil || started > 0 || isNew {
		return err
	}

	if _, recorded, err := s.Thread(conversationID); err != nil || recorded {
		return err
	}

	return fmt.Errorf("continue ID %q has no saved subagent history in this conversation", strings.TrimPrefix(conversationID, parent))
}

// subagentLineage returns the top-level session the subagent session conversationID belongs to, the turn
// the subagent acts for: that session's running turn and its inbound or, with no turn running, the routing
// that session keeps as a hidden run, and, for each delegation from that session down to conversationID, the
// task job whose input the delegated session's running turn runs, with no job ID when it runs no turn.
func (s *SessionService) subagentLineage(ctx context.Context, conversationID string) (root, turnID string, origin *protocol.InboundMessage, jobs []backgroundKey, err error) {
	var inbound, inputs []byte
	if err := s.db.QueryRowContext(ctx, `WITH RECURSIVE lineage AS (
    SELECT conversation_id, parent_conversation_id, producer_inbound_json FROM managed_conversations WHERE conversation_id = $1
    UNION ALL SELECT c.conversation_id, c.parent_conversation_id, c.producer_inbound_json FROM managed_conversations c JOIN lineage l ON c.conversation_id = l.parent_conversation_id
), turns AS (
    SELECT l.*, t.id, t.inbound_json FROM lineage l LEFT JOIN active_turns t ON t.conversation_id = l.conversation_id AND t.phase = $2
), inputs AS (
    SELECT COALESCE(jsonb_agg(jsonb_build_array(parent_conversation_id, COALESCE(inbound_json->'Metadata'->>'web_message_id', ''))) FILTER (WHERE parent_conversation_id <> ''), '[]') AS pairs FROM turns
)
SELECT t.conversation_id, COALESCE(t.id, ''), COALESCE(t.inbound_json, t.producer_inbound_json, '{}'), i.pairs
FROM turns t CROSS JOIN inputs i WHERE t.parent_conversation_id = ''`, conversationID, turnRunning).Scan(&root, &turnID, &inbound, &inputs); err != nil {
		return "", "", nil, nil, fmt.Errorf("read subagent lineage: %w", err)
	}

	var pairs [][2]string
	if err := errors.Join(json.Unmarshal(inbound, &origin), json.Unmarshal(inputs, &pairs)); err != nil {
		return "", "", nil, nil, fmt.Errorf("decode subagent lineage: %w", err)
	}

	for _, pair := range pairs {
		jobs = append(jobs, backgroundKey{pair[0], strings.TrimPrefix(pair[1], pair[0]+"/")})
	}

	return root, turnID, origin, jobs, nil
}

// checkSessionTarget refuses a write target that is not a top-level session, that is the
// calling turn's own session, or, for children, that the caller's top-level session did not create.
func (s *SessionService) checkSessionTarget(ctx context.Context, children bool, owner, syncDestination, target string) error {
	spawnedBy, err := s.topLevelSessionCreator(ctx, target)
	if err != nil {
		return err
	}

	if target == owner || target == syncDestination || s.reservedFor(target) == owner {
		return fmt.Errorf("session %q is this session", target)
	}

	if children && spawnedBy != cmp.Or(syncDestination, owner) {
		return fmt.Errorf("session %q was not created by this session", target)
	}

	return nil
}

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
		Name: sessionsListToolName, Description: fmt.Sprintf("List durable conversations of this State Store, newest first. Supply all five fields: criteria, since (required), until (empty for no upper bound), limit (1 to %d) and include_message_preview. Returns TSV with a header, one session per row with entry counts, timestamps and optional message previews. "+
			"Empty criteria lists every stored conversation by latest entry, including private MCP and cron sessions. "+
			"Other criteria is a Web /search query and returns what /search would: only the chats Web lists, never private External MCP or cron producer chats, filtered before the limit; since, until and order then use each chat's summary time. "+
			"Query language: tag:V, cron:V, agent:V and room:V match V exactly and case-sensitively; model:V matches part of the agent's configured model, ignoring case; V may be a JSON-quoted string such as tag:\"Needs review\"; is:pinned, is:forked and is:cron; sort:newest or sort:oldest. Room terms match either room; other filters use AND. Unknown or malformed terms are searched as text. "+
			"All remaining text is ONE case-insensitive phrase, not separate keywords, matched against name, room, agent, preview, session label, chat origin and message text. "+
			"With criteria, rows add name, agent, room, tags and matched (the field holding the phrase, or messages when only message text did), and a messages block lists up to 3 matching messages per listed chat; rocketclaw_sessions_get with a row's before_entry_id and limit 1 shows that message. Messages of stopped turns are not listed. "+
			"Trailing lines show how criteria was read and whether the limit cut results or the message index or session summaries were incomplete.", maxListedSessions),
		Permission: "rocketclaw", VisibilitySubjects: []string{sessionsListToolName},
		Subjects: func(json.RawMessage) ([]string, error) { return []string{sessionsListToolName}, nil },
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
				ids, err = queryStrings(ctx, service.db, `SELECT conversation_id FROM session_entries e
WHERE NOT EXISTS (SELECT 1 FROM shadow_bindings b WHERE b.child_conversation_id = e.conversation_id OR starts_with(e.conversation_id, b.child_conversation_id || '/'))
GROUP BY conversation_id
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
					// A stopped turn's hit has no message ID, so rocketclaw_sessions_get cannot open it.
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
	search, err := s.SearchSessions(ctx, criteria, SearchMessages, slack)
	if err != nil {
		return nil, nil, err
	}

	var matches []SessionMatch

	for i := range search.Matches {
		// Summary times compare as the bounds do: UTC RFC3339Nano text without Z.
		// Remote OpenCode sessions are for people only; agents never see them.
		if summary := search.Matches[i].Chat.Session.Summary; summary != nil && search.Matches[i].Chat.Session.Server == "" {
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
		Name: sessionsCurrentIDToolName, Description: "Return only the owning RocketClaw bridge's bare stored conversation ID, without a JSON wrapper. Pass it as conversation_id to rocketclaw_sessions_get inside execute to read durable history, including entries before compaction. Child agents inherit this owning conversation ID, not a separate child-run ID. No arguments.",
		Permission: "rocketclaw", VisibilitySubjects: []string{sessionsCurrentIDToolName},
		Subjects:   func(json.RawMessage) ([]string, error) { return []string{sessionsCurrentIDToolName}, nil },
		Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false},
		Call: func(context.Context, json.RawMessage, chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
			return rocketcode.TextToolResult(conversationID), nil
		},
	}
}

func sessionsGetURLTool(threads *threadBridgeManager) rocketcode.Tool {
	return rocketcode.Tool{
		Name: sessionsGetURLToolName, Description: "Return only the clickable Web URL of a top-level session, one Web lists. Fails for Delegation Histories, private External MCP and cron producer conversations, and unknown IDs.",
		Permission: "rocketclaw", VisibilitySubjects: []string{sessionsGetURLToolName},
		Subjects:   func(json.RawMessage) ([]string, error) { return []string{sessionsGetURLToolName}, nil },
		Parameters: map[string]any{"properties": map[string]any{"session_id": map[string]any{"type": "string"}}, "required": []string{"session_id"}},
		Call: func(ctx context.Context, raw json.RawMessage, _ chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
			var params struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(raw, &params); err != nil {
				return rocketcode.ToolResult{}, fmt.Errorf("parse get session URL: %w", err)
			}

			if _, err := threads.store.topLevelSessionCreator(ctx, params.SessionID); err != nil {
				return rocketcode.ToolResult{}, err
			}

			url, err := threads.WebURL(ctx, params.SessionID)
			if err != nil {
				return rocketcode.ToolResult{}, err
			}

			return rocketcode.TextToolResult(url), nil
		},
	}
}

// sessionWriteMode is how rocketclaw_sessions_write delivers into an existing session.
type sessionWriteMode string

const (
	sessionWriteStop    sessionWriteMode = "stop"
	sessionWriteSteer   sessionWriteMode = "steer"
	sessionWriteEnqueue sessionWriteMode = "enqueue"
	sessionWriteStash   sessionWriteMode = "stash"
)

// sessionWriteOutcome is what became of a rocketclaw_sessions_write call.
type sessionWriteOutcome string

const (
	sessionCreated sessionWriteOutcome = "created"
	sessionSteered sessionWriteOutcome = "steered"
	sessionQueued  sessionWriteOutcome = "queued"
	sessionStashed sessionWriteOutcome = "stashed"
	sessionStopped sessionWriteOutcome = "stopped"
)

// stopLineStep prefixes the turn steps that park a stop write's "Stopped by" line for its session.
const stopLineStep = "stopped_by/"

// postStopLines moves conversationID's parked "Stopped by" lines into its history unless a turn
// still runs there. Under the history lock a turn's finish also takes, each line posts once, after
// the turn it stopped; the bridge calls this when it starts and after each request.
func (s *SessionService) postStopLines(ctx context.Context, conversationID string) error {
	tx, err := s.beginStateTx(ctx, "post stop lines")
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(ctx, tx, conversationID); err != nil {
		return err
	}

	lines, err := queryRows(ctx, tx, `WITH running AS (SELECT count(*) AS turns FROM active_turns WHERE conversation_id = $1 AND phase = $3),
parked AS (DELETE FROM turn_steps USING running WHERE conversation_id = $1 AND starts_with(key, $2) AND turns = 0 RETURNING value)
SELECT value FROM parked ORDER BY (value::jsonb->>'timestamp')::timestamptz`, "parked stop lines", func(row rowScanner) (line rocketcode.SessionEntry, err error) {
		var raw []byte
		if err := row.Scan(&raw); err != nil {
			return line, fmt.Errorf("scan parked stop line: %w", err)
		}

		if err := json.Unmarshal(raw, &line); err != nil {
			return line, fmt.Errorf("decode parked stop line: %w", err)
		}

		return line, nil
	}, conversationID, stopLineStep, turnRunning)
	if err != nil {
		return err
	}

	for i := range lines {
		if _, err := appendSessionEntryDB(ctx, tx, conversationID, conversationID, &lines[i]); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit stop lines: %w", err)
	}

	return nil
}

// sessionsWriteTool builds rocketclaw_sessions_write for a turn of b acting as the turn agentName runs
// for msg in the session self: b's own turn or, for a subagent session, its top-level session's, and for
// the destination msg reports to as a hidden run. It never waits for the target's turn.
func sessionsWriteTool(b *Bridge, msg *protocol.InboundMessage, agentName, self string) rocketcode.Tool {
	owner := cmp.Or(msg.SyncDestination, self)
	properties := map[string]any{"session_id": map[string]any{"type": "string"}, "mode": map[string]any{"type": "string", "enum": []sessionWriteMode{sessionWriteStop, sessionWriteSteer, sessionWriteEnqueue, sessionWriteStash}}}

	for _, field := range []string{"prompt", "title", "agent"} {
		properties[field] = map[string]any{"type": "string"}
	}

	return rocketcode.Tool{Name: sessionsWriteToolName, Permission: "rocketclaw", VisibilitySubjects: []string{sessionsWriteToolName}, Subjects: func(json.RawMessage) ([]string, error) { return []string{sessionsWriteToolName}, nil },
		Description: "Create a top-level RocketClaw Web session, or write prompt to an existing one, without waiting for its turn. Supply every field. " +
			"With an empty session_id, create a session named title, run by agent (empty for this turn's agent), whose first task is prompt; mode is ignored. The new session cannot see this conversation, so prompt must carry all the context it needs. " +
			"With session_id, deliver prompt to that session; title and agent are ignored. steer joins its running turn while that turn still takes input, otherwise runs as its next turn; enqueue runs after the work already queued; stash waits in its Web queue until a person releases it; stop stops its current turn and goal, then runs prompt before anything already queued. " +
			"Only stop takes an empty prompt: it then just stops the turn and goal, queues nothing, and posts that this session stopped it into that session's transcript without starting a turn. " +
			"The receiver is told this session and agent sent it. Returns JSON with outcome (created, steered, queued, stashed or stopped), session_id, url, queue_item_id when a prompt was delivered and, for stop, turn_was_running. Share url so a person can open the session.",
		Parameters: map[string]any{"properties": properties, "required": slices.Sorted(maps.Keys(properties))},
		Call: func(ctx context.Context, raw json.RawMessage, _ chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
			var input struct {
				SessionID string           `json:"session_id"`
				Mode      sessionWriteMode `json:"mode"`
				Prompt    string           `json:"prompt"`
				Title     string           `json:"title"`
				Agent     string           `json:"agent"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return rocketcode.ToolResult{}, fmt.Errorf("parse %s: %w", sessionsWriteToolName, err)
			}

			stopOnly := strings.TrimSpace(input.Prompt) == ""
			if stopOnly && (input.SessionID == "" || input.Mode != sessionWriteStop) {
				return rocketcode.ToolResult{}, errors.New("prompt is required")
			}

			agent, _ := rocketcode.ToolCallAgent(ctx)
			inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, input.Prompt, false)
			inbound.PreserveWhitespace = true
			inbound.Metadata = map[string]string{protocol.InboundOriginMetadataKey: "System", protocol.InboundMediaMetadataKey: "Text", protocol.InboundSenderSessionMetadataKey: owner, protocol.InboundSenderAgentMetadataKey: agent.Name}

			var (
				result struct {
					Outcome        sessionWriteOutcome `json:"outcome"`
					SessionID      string              `json:"session_id"`
					URL            string              `json:"url"`
					QueueItemID    string              `json:"queue_item_id,omitempty"`
					TurnWasRunning *bool               `json:"turn_was_running,omitempty"`
				}
				running bool
				err     error
			)

			if input.SessionID == "" {
				title := strings.TrimSpace(input.Title)
				if title == "" {
					return rocketcode.ToolResult{}, errors.New("title is required to create a session")
				}

				allowed := strings.FieldsFunc(msg.Metadata[protocol.InboundAllowedAgentsMetadataKey], func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' ' })
				start := threadStart{agent: cmp.Or(strings.TrimSpace(input.Agent), agentName), createdBy: ThreadCreator(msg.Metadata[protocol.InboundPrincipalMetadataKey]), spawnedBy: owner}

				if msg.Cronjob != nil {
					allowed, start.createdBy = []string{agentName}, ThreadCreatedByCron
				}

				result.Outcome = sessionCreated
				result.URL, err = b.threads.createSession(ctx, &start, allowed, title, inbound)
				result.SessionID = start.conversationID
			} else {
				// RocketCode loaded permission.rocketclaw.rocketclaw_sessions_write: children as allow.
				permission, _ := agent.Frontmatter["permission"].(map[string]any)
				rocketclaw, _ := permission["rocketclaw"].(map[string]any)

				if err := b.config.SessionService.checkSessionTarget(ctx, rocketclaw[sessionsWriteToolName] == "children", self, msg.SyncDestination, input.SessionID); err != nil {
					return rocketcode.ToolResult{}, err
				}

				// The link is looked up before delivery, so a failed lookup delivers nothing.
				result.SessionID = input.SessionID
				if result.URL, err = b.threads.WebURL(ctx, input.SessionID); err != nil {
					return rocketcode.ToolResult{}, err
				}

				result.Outcome = sessionStopped

				if stopOnly {
					// A developer message is history the next turn reads and Web shows; posting it starts no turn.
					// It is parked only after the stop succeeds and posts once no turn runs there: here if the
					// stopped turn already ended, otherwise when the bridge flushes at that turn's end.
					line, _ := json.Marshal(&rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now().UTC(), ReplayInput: replayInputForMessage("developer", fmt.Sprintf("Stopped by session %s (agent %s)", owner, agent.Name))}) // Strings and messages cannot fail to encode.
					if running, err = b.threads.stopSession(input.SessionID); err == nil {
						err = b.config.SessionService.SaveTurnStep(ctx, input.SessionID, stopLineStep+b.config.ConversationID+"/"+rocketcode.ToolCallKey(ctx), line)
					}

					if err == nil {
						err = b.config.SessionService.postStopLines(ctx, input.SessionID)
					}
				} else {
					result.QueueItemID = b.config.ConversationID + "/" + rocketcode.ToolCallKey(ctx)
					result.Outcome, running, err = b.threads.writeSession(ctx, input.SessionID, result.QueueItemID, input.Mode, inbound)
				}

				if result.Outcome == sessionStopped {
					result.TurnWasRunning = &running
				}
			}

			if err != nil {
				return rocketcode.ToolResult{}, err
			}

			data, _ := json.Marshal(result) // Strings and a bool cannot fail to encode.

			return rocketcode.TextToolResult(string(data)), nil
		}}
}

// storedEventLine renders a stored item that is not a replay message: a Code Mode record shows the
// call an Execute script made, or the approver's decision on the whole script; anything else is a
// placeholder naming its kind.
func storedEventLine(kind string, raw json.RawMessage) (role, text string) {
	if calls := rocketcode.CodeModeCallsFromTrace([]json.RawMessage{raw}); len(calls) > 0 {
		return "code_mode", calls[0].String()
	}

	return "event", "[" + kind + ": stored event not rendered]"
}

func getSessionTool(service *SessionService) rocketcode.Tool {
	return rocketcode.Tool{
		Name: sessionsGetToolName, Description: fmt.Sprintf("Read the newest limit (1 to %d) stored entries of a durable conversation older than before_entry_id (0 for the newest) as timestamp, role and content TSV in stored entry order, including messages, readable reasoning and tool events before compaction. When older entries exist, the last line is [next_before_entry_id=N]; pass N to read the previous page. Store-wide access includes private MCP sessions. One snapshot, no polling or read-state changes. Use rocketclaw_sessions_current_id for your owning conversation or rocketclaw_sessions_list to find another ID.", maxSessionEntries),
		Permission: "rocketclaw", VisibilitySubjects: []string{sessionsGetToolName},
		Subjects: func(json.RawMessage) ([]string, error) { return []string{sessionsGetToolName}, nil },
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
							role, text := storedEventLine(kind, raw)
							fmt.Fprintf(&output, "%s\t%s\t%s\n", timestamp, role, escape.Replace(text))

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

			return sessionToolComparisonResult(ctx, service.db, params.ConversationID, params.BeforeEntryID, &output)
		},
	}
}

// sessionToolComparisonResult formats binding ownership separately from the
// replay decoder so comparison inspection does not complicate message decoding.
func sessionToolComparisonResult(ctx context.Context, db stateStoreDB, id string, before int64, output *strings.Builder) (rocketcode.ToolResult, error) {
	shadows, err := shadowHistoryDB(ctx, db, id, "", 0, before)
	if err != nil {
		return rocketcode.ToolResult{}, err
	}

	if len(shadows) > 0 {
		escape := strings.NewReplacer("\\", "\\\\", "\t", "\\t", "\r", "\\r", "\n", "\\n")

		output.WriteString("\nshadows\nparent_conversation_id\tparent_agent\tshadow_agent\tchild_conversation_id\n")

		for _, shadow := range shadows {
			fmt.Fprintf(output, "%s\t%s\t%s\t%s\n", escape.Replace(shadow.ParentConversationID), escape.Replace(shadow.ParentAgent), escape.Replace(shadow.ShadowAgent), escape.Replace(shadow.ChildConversationID))
		}
	}

	return rocketcode.TextToolResult(output.String()), nil
}
