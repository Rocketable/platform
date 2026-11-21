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

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"golang.org/x/sync/errgroup"
)

const (
	sessionsListToolName      = "rocketclaw_sessions_list"
	sessionsGetToolName       = "rocketclaw_sessions_get"
	sessionsCurrentIDToolName = "rocketclaw_sessions_current_id"
	setTagToolName            = "rocketclaw_set_tag"
	getTagsToolName           = "rocketclaw_get_tags"
	sessionsWriteToolName     = "rocketclaw_sessions_write"
	sessionsGetURLToolName    = "rocketclaw_sessions_get_url"
	sessionsMoveToolName      = "rocketclaw_sessions_move"
	sessionsSubject           = "rocketclaw_sessions"
	recipientSetting          = "rocketclaw_sessions_recipient"

	maxListedSessions = 200
	maxSessionEntries = 100
	summaryReaders    = 8
)

func (s *SessionService) checkTopLevelSession(ctx context.Context, id string) (string, error) {
	var agent string

	err := queryScalar(ctx, s.db, fmt.Sprintf("read session %q", id), `WITH `+visibleChatsSQL+` SELECT agent FROM visible_chats WHERE conversation_id = $1`, &agent, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("session %q is not a top-level session", id)
	}

	return agent, err
}

func (s *SessionService) startSubagent(ctx context.Context, conversation string, write *harness.SubagentWrite) error {
	conversationID, parent, isNew := conversation+write.Key.Path, conversation+write.Key.Parent, write.Mode == ""

	tools := ""

	if write.Tools != nil {
		encoded, _ := json.Marshal(write.Tools)
		tools = string(encoded)
	}

	started, err := execRows(ctx, s.db, "start subagent session", `WITH saved AS (
    SELECT count(*) AS entries FROM entries WHERE conversation_id = $1
), recorded AS (
    SELECT count(*) AS sessions FROM conversations WHERE conversation_id = $1 AND retention = 'record'
), parent AS (
    SELECT max(slack_channel) AS slack_channel FROM conversations WHERE conversation_id = $2
)
INSERT INTO conversations (conversation_id, agent, created_by, parent_conversation_id, kind, listed, slack_channel, call_turn, call_id, subagent_model, subagent_tools)
SELECT $1, $3, '', $2, $5, FALSE, p.slack_channel, $6, $7, NULLIF($8, ''), NULLIF($9, '')::jsonb FROM saved, recorded, parent p WHERE $4 OR entries > 0 OR sessions > 0
ON CONFLICT (conversation_id) DO UPDATE SET agent = excluded.agent, kind = excluded.kind, retention = 'record', subagent_model = excluded.subagent_model, subagent_tools = excluded.subagent_tools`,
		conversationID, parent, write.Agent, isNew, KindSubagent, write.Key.Turn, write.Key.Call, write.Model, tools)
	if err != nil || started > 0 || isNew {
		return err
	}

	return fmt.Errorf("continue ID %q has no saved subagent history in this conversation", write.Key.Path)
}

func (s *SessionService) subagentLineage(ctx context.Context, conversationID string) (root, turnID string, origin *protocol.InboundMessage, jobs []backgroundKey, err error) {
	var inbound, inputs []byte
	if err := s.db.QueryRowContext(ctx, `WITH RECURSIVE lineage AS (
    SELECT conversation_id, parent_conversation_id, producer_inbound_json, reports_to FROM conversations WHERE conversation_id = $1
    UNION ALL SELECT c.conversation_id, c.parent_conversation_id, c.producer_inbound_json, c.reports_to FROM conversations c JOIN lineage l ON c.conversation_id = l.parent_conversation_id
), lineage_turns AS (
    SELECT l.*, t.id, t.inbound_json FROM lineage l LEFT JOIN turns t ON t.conversation_id = l.conversation_id AND t.phase = $2 AND t.takes_lane
), inputs AS (
    SELECT COALESCE(jsonb_agg(jsonb_build_array(parent_conversation_id, COALESCE(inbound_json->'Metadata'->>'web_message_id', ''))) FILTER (WHERE parent_conversation_id IS NOT NULL), '[]') AS pairs FROM lineage_turns
)
SELECT t.conversation_id, COALESCE(t.id, ''), COALESCE(t.inbound_json::jsonb,
    jsonb_set(t.producer_inbound_json::jsonb, '{SyncDestination}', to_jsonb(COALESCE(t.reports_to, ''))), '{}'), i.pairs
FROM lineage_turns t CROSS JOIN inputs i WHERE t.parent_conversation_id IS NULL`, conversationID, turnRunning).Scan(&root, &turnID, &inbound, &inputs); err != nil {
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

func (s *SessionService) checkSessionTarget(ctx context.Context, owner, syncDestination, target string) (string, error) {
	agent, err := s.checkTopLevelSession(ctx, target)
	if err != nil {
		return "", err
	}

	var reportsTo string
	if err := queryScalar(ctx, s.db, "read session destination", `SELECT COALESCE(max(reports_to), '') FROM conversations WHERE conversation_id = $1`, &reportsTo, owner); err != nil {
		return "", err
	}

	if target == owner || target == syncDestination || target == reportsTo {
		return "", fmt.Errorf("session %q is this session", target)
	}

	return agent, nil
}

func agentTagGroups(agent *harness.Agent) ([][]string, error) {
	groups := agent.TagGroups
	if agent.Mode != harness.AgentModeEasy {
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

		if err := json.Unmarshal(data, &groups); err != nil {
			return nil, fmt.Errorf("decode tag groups: %w", err)
		}

		if groups == nil {
			return nil, errors.New("tags must be a list of tag lists")
		}
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

func agentRecipients(agent *harness.Agent) (all bool, names []string, err error) {
	if agent.Mode == harness.AgentModeEasy {
		return agent.DelegatedFrom == nil, agent.DelegatedFrom, nil
	}

	permission, _ := agent.Frontmatter["permission"].(map[string]any)
	rocketclaw, _ := permission["rocketclaw"].(map[string]any)

	raw, present := rocketclaw[recipientSetting]
	if !present {
		return !agent.Permission.Sudo, nil, nil
	}

	if all, ok := raw.(bool); ok {
		return all, nil, nil
	}

	list, _ := raw.([]any)
	if len(list) == 0 {
		return false, nil, errors.New("must be true, false, or a non-empty list of agent names")
	}

	for _, item := range list {
		name, _ := item.(string)
		if name == "" || slices.Contains(names, name) {
			return false, nil, fmt.Errorf("agent names must be unique non-empty strings, got %v", item)
		}

		names = append(names, name)
	}

	return false, names, nil
}

func checkedAgentTagGroups(agent *harness.Agent) ([][]string, error) {
	tagsKey := "permission.rocketclaw." + setTagToolName
	if agent.Mode == harness.AgentModeEasy {
		tagsKey = "tags"
	}

	groups, err := agentTagGroups(agent)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", tagsKey, err)
	}

	if _, _, err := agentRecipients(agent); err != nil {
		return nil, fmt.Errorf("permission.rocketclaw.%s: %w", recipientSetting, err)
	}

	if agent.Approver != nil {
		if _, err := agentTagGroups(agent.Approver); err != nil {
			return nil, fmt.Errorf("approver.tags: %w", err)
		}
	}

	return groups, nil
}

func checkRecipient(agents harness.Agents, target, sender string) error {
	definition := agents.Items[target]
	if all, names, _ := agentRecipients(&definition); all || slices.Contains(names, sender) {
		return nil
	}

	setting := "permission.rocketclaw." + recipientSetting
	if definition.Mode == harness.AgentModeEasy {
		setting = "delegatedFrom"
	}

	return fmt.Errorf("agent %q does not accept writes from agent %q (%s)", target, sender, setting)
}

func setSessionTagTools(agent *harness.Agent, groups [][]string) {
	action := harness.PermissionDeny
	if len(groups) > 0 && (agent.Permission.Sudo || agent.Permission.Easy) {
		action = harness.PermissionAuto
	} else if len(groups) > 0 {
		action = harness.PermissionAllow
	}

	agent.Permission.Buckets = append(agent.Permission.Buckets, harness.PermissionBucket{Name: "rocketclaw_tags", Rules: []harness.PermissionRule{{Pattern: setTagToolName, Action: action}, {Pattern: getTagsToolName, Action: action}}})

	var tools []string

	for _, name := range []string{setTagToolName, getTagsToolName} {
		if agent.Permission.Enabled("rocketclaw_tags", name) {
			tools = append(tools, name)
		}
	}

	if len(tools) == 0 {
		return
	}

	agent.Prompt += "\n\n## Session Tags\n\nAvailable tools: " + strings.Join(tools, ", ") + ". Tags belong to the owning session and persist across restarts. Allowed literal, case-sensitive groups: " + fmt.Sprintf("%q", groups) + ". Each group is exclusive: set an inactive tag to replace its group; set an active tag to toggle it off. Other groups stay unchanged. Get returns all active session tags."
}

func sessionTagTools(service *SessionService, conversationID string) []harness.Tool {
	tools := make([]harness.Tool, 0, 2)

	for _, name := range []string{setTagToolName, getTagsToolName} {
		parameters := map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false}
		description := "Return all durable active tags of the owning session as {\"tags\":[...]}. No arguments."

		if name == setTagToolName {
			parameters["properties"] = map[string]any{"tag": map[string]any{"type": "string"}}
			parameters["required"] = []string{"tag"}
			description = "Toggle a permitted literal tag on the owning session. An inactive tag replaces its exclusive group; an active tag is removed. Other groups stay unchanged. Return {\"tags\":[...]} in lexical order."
		}

		tools = append(tools, harness.Tool{Name: name, Description: description, Permission: "rocketclaw_tags", VisibilitySubjects: []string{name}, Subjects: func(json.RawMessage) ([]string, error) { return []string{name}, nil }, Parameters: parameters,
			Call: func(ctx context.Context, raw json.RawMessage, _ chan<- harness.ChatResponse) (harness.ToolResult, error) {
				agent, ok := harness.ToolCallAgent(ctx)
				if !ok {
					return harness.ToolResult{}, errors.New("tag tools require an active agent call")
				}

				groups, err := agentTagGroups(&agent)
				if err != nil {
					return harness.ToolResult{}, err
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
					return harness.ToolResult{}, fmt.Errorf("parse tag arguments: %w", err)
				}

				if !json.Valid(raw) || name == getTagsToolName && empty == nil || name == setTagToolName && (params == nil || params.Tag == nil) {
					return harness.ToolResult{}, errors.New("invalid tag arguments")
				}

				var tags []string

				if name == setTagToolName {
					group := slices.IndexFunc(groups, func(group []string) bool { return slices.Contains(group, *params.Tag) })
					if group < 0 {
						return harness.ToolResult{}, fmt.Errorf("tag %q is not permitted for agent %q", *params.Tag, agent.Name)
					}

					tags, err = service.toggleSessionTag(ctx, conversationID, *params.Tag, groups[group])
				} else {
					tags, err = sessionTags(ctx, service.db, conversationID)
				}

				if err != nil {
					return harness.ToolResult{}, err
				}

				result, _ := json.Marshal(struct {
					Tags []string `json:"tags"`
				}{tags})

				return harness.TextToolResult(string(result)), nil
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

const sessionListCandidatesSQL = `WITH RECURSIVE runs AS (
    SELECT conversation_id, reports_to FROM conversations WHERE reports_to IS NOT NULL
), viewed AS (
    SELECT conversation_id, entry_timestamp FROM entries
    UNION ALL SELECT r.reports_to, e.entry_timestamp FROM runs r JOIN entries e USING (conversation_id)
    UNION ALL SELECT c.conversation_id, e.entry_timestamp FROM conversations c JOIN entries e ON e.conversation_id = c.continues
    WHERE c.continues IS NOT NULL
), recent AS (
    SELECT conversation_id, MAX(rtrim(entry_timestamp, 'Z') COLLATE "C") AS latest FROM viewed GROUP BY conversation_id
    HAVING MAX(rtrim(entry_timestamp, 'Z') COLLATE "C") >= $1 COLLATE "C"
    AND ($2 = '' OR MAX(rtrim(entry_timestamp, 'Z') COLLATE "C") < $2 COLLATE "C")
), starts AS (
    SELECT conversation_id AS id FROM recent
), ` + ancestorsSQL + `, shadowed AS (
    SELECT DISTINCT a.id AS conversation_id FROM ancestors a JOIN conversations b ON b.conversation_id = a.conversation_id WHERE b.shadow_parent IS NOT NULL
)
SELECT r.conversation_id FROM recent r LEFT JOIN shadowed s USING (conversation_id) WHERE s.conversation_id IS NULL
ORDER BY r.latest DESC, r.conversation_id COLLATE "C" LIMIT $3`

func listSessionsTool(service *SessionService, threads *threadBridgeManager) harness.Tool {
	return harness.Tool{
		Name: sessionsListToolName, Description: fmt.Sprintf("List durable conversations of this State Store, newest first. Supply all five fields: criteria, since (required), until (empty for no upper bound), limit (1 to %d) and include_message_preview. Returns TSV with a header, one session per row with entry counts, timestamps and optional message previews. "+
			"Empty criteria lists every stored conversation by latest entry, including private MCP and cron sessions. "+
			"Other criteria is a Web /search query and returns what /search would: only the chats Web lists, never private External MCP or cron producer chats, filtered before the limit; order then uses each chat's summary time, and since and until the later of it and when the chat's newest running or waiting turn was created, so current work counts as recent. "+
			"Query language: tag:V, cron:V, agent:V and room:V match V exactly and case-sensitively; space:V matches the complete Birth Space name ignoring case, not tab placement; model:V matches part of the agent's configured model, ignoring case; V may be a JSON-quoted string such as tag:\"Needs review\"; is:forked and is:cron; is:running (a running turn or background work), is:queued (waiting turns other than scheduled ones), is:waiting (waiting on a person: a pending question, form or permission request) and is:external (bound to External MCP), all but is:waiting counting the hidden runs reporting to a chat as its own; sort:newest or sort:oldest. agent:V also matches chats showing a hidden cron or External MCP run of agent V. Room terms match any one room and agent terms any one agent; other filters use AND. Unknown or malformed terms are searched as text. "+
			"All remaining text is ONE case-insensitive phrase, not separate keywords, matched against name, room, agent, preview, session label, chat origin and message text. "+
			"With criteria, rows add name, agent, room, tags and matched (the field holding the phrase, or messages when only message text did), and a messages block lists up to 3 matching messages per listed chat; rocketclaw_sessions_get with a row's before_entry_id and limit 1 shows that message. Messages of stopped turns are not listed. "+
			"Trailing lines show how criteria was read and whether the limit cut results.", maxListedSessions),
		Permission: "rocketclaw", VisibilitySubjects: []string{sessionsSubject},
		Subjects: func(json.RawMessage) ([]string, error) { return []string{sessionsSubject}, nil },
		Parameters: map[string]any{"properties": map[string]any{
			"criteria":                map[string]any{"type": "string", "description": "Web session search query, or empty for every stored conversation."},
			"since":                   map[string]any{"type": "string", "description": "Required inclusive bound on the latest entry, or on the summary time with criteria: Go duration relative to now or RFC3339Nano."},
			"until":                   map[string]any{"type": "string", "description": "Exclusive bound like since: RFC3339Nano, or empty for no bound."},
			"limit":                   map[string]any{"type": "integer", "minimum": 1, "maximum": maxListedSessions},
			"include_message_preview": map[string]any{"type": "boolean"},
		}, "required": []string{"criteria", "since", "until", "limit", "include_message_preview"}},
		Call: func(ctx context.Context, raw json.RawMessage, _ chan<- harness.ChatResponse) (harness.ToolResult, error) {
			var params sessionListParams
			if err := json.Unmarshal(raw, &params); err != nil {
				return harness.ToolResult{}, fmt.Errorf("parse list sessions: %w", err)
			}

			if params.Limit < 1 || params.Limit > maxListedSessions {
				return harness.ToolResult{}, fmt.Errorf("limit must be between 1 and %d", maxListedSessions)
			}

			now := time.Now().UTC()

			params.Since, params.Until = strings.TrimSpace(params.Since), strings.TrimSpace(params.Until)
			if params.Since == "" {
				return harness.ToolResult{}, errors.New("since is required")
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
						return harness.ToolResult{}, fmt.Errorf("parse session time bound: %w", err)
					}

					at = parsed
				}

				*bound = strings.TrimSuffix(at.UTC().Format(time.RFC3339Nano), "Z")
			}

			var (
				ids    []string
				search *SessionSearch
				shown  = make(map[string]int)
				err    error
			)

			if criteria := strings.TrimSpace(params.Criteria); criteria == "" {
				ids, err = queryStrings(ctx, service.db, sessionListCandidatesSQL, "session tool candidates", params.Since, params.Until, params.Limit)
			} else {
				threads.mu.Lock()
				slack := threads.slackLookups
				threads.mu.Unlock()

				search, err = service.searchSessionsWithin(ctx, criteria, &params, slack)
			}

			if err != nil {
				return harness.ToolResult{}, err
			}

			var output strings.Builder

			escape := strings.NewReplacer("\\", "\\\\", "\t", "\\t", "\r", "\\r", "\n", "\\n")

			output.WriteString("conversation_id\tturns\tlast_updated")

			if search != nil {
				for i := range search.Matches[:min(len(search.Matches), params.Limit)] {
					ids = append(ids, search.Matches[i].Chat.Session.Conversation.ID)
					shown[ids[i]] = 0
				}

				output.WriteString("\tname\tagent\troom\ttags\tmatched")
			}

			if params.IncludeMessagePreview {
				output.WriteString("\tlast_user_message\tlast_assistant_message")
			}

			output.WriteByte('\n')

			summaries, err := service.sessionToolSummaries(ctx, ids, params.IncludeMessagePreview)
			if err != nil {
				return harness.ToolResult{}, err
			}

			for i, summary := range summaries {
				var columns string

				if search != nil {
					chat := &search.Matches[i].Chat

					matched := cmp.Or(string(search.Matches[i].Field), "messages")
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
					if count, listed := shown[hit.ConversationID]; !listed || count == 3 || hit.MessageID == "" {
						continue
					}

					shown[hit.ConversationID]++
					entry, _, _ := strings.Cut(hit.MessageID, ":")
					id, _ := strconv.ParseInt(entry, 10, 64)

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

				if more := len(search.Matches) - len(ids); more > 0 {
					fmt.Fprintf(&output, "[truncated: %d more matching sessions]\n", more)
				}
			}

			return harness.TextToolResult(output.String()), nil
		},
	}
}

func (s *SessionService) searchSessionsWithin(ctx context.Context, criteria string, params *sessionListParams, slack slackLookup) (*SessionSearch, error) {
	search, err := s.SearchSessions(ctx, "", criteria, SearchMessages, slack)
	if err != nil {
		return nil, err
	}

	search.Matches = slices.DeleteFunc(search.Matches, func(match SessionMatch) bool {
		row := &match.Chat.Session
		if row.Summary == nil || row.Server != "" {
			return true
		}

		at := strings.TrimSuffix(slices.MaxFunc([]time.Time{row.Summary.LastUpdated, row.ArrivedAt}, time.Time.Compare).Format(time.RFC3339Nano), "Z")

		return at < params.Since || params.Until != "" && at >= params.Until
	})

	if !slices.ContainsFunc(search.Terms, func(term SearchTerm) bool { return term.Key == keySort }) {
		slices.SortFunc(search.Matches, func(a, b SessionMatch) int {
			return cmp.Or(b.Chat.Session.Summary.LastUpdated.Compare(a.Chat.Session.Summary.LastUpdated), strings.Compare(a.Chat.Session.Conversation.ID, b.Chat.Session.Conversation.ID))
		})
	}

	return search, nil
}

func (s *SessionService) sessionToolSummaries(ctx context.Context, ids []string, preview bool) ([]sessionListSummary, error) {
	summaries := make([]sessionListSummary, len(ids))
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(summaryReaders)

	for i, id := range ids {
		group.Go(func() error {
			summary := &summaries[i]
			summary.ConversationID = id

			for _, limit := range []int{1, 0} {
				err := readRows(ctx, s.db, `WITH `+sessionHistorySQL+`, counted AS (
    SELECT id, entry_json, count(*) OVER () AS turns FROM viewed_rows
), newest AS (
    SELECT c.*, e.entry_timestamp FROM counted c JOIN entries e USING (id) ORDER BY c.id DESC LIMIT NULLIF($2, 0)
), fields AS (
    SELECT n.id, n.turns, COALESCE(f."timestamp", n.entry_timestamp) AS "timestamp", f.replay_input FROM newest n,
        json_to_record(CASE WHEN $3 THEN n.entry_json END) AS f("timestamp" text, replay_input json)
)
SELECT f.turns, json_build_object('timestamp', f."timestamp", 'replay_input',
    json_agg(i.item ORDER BY i.n) FILTER (WHERE i.n IS NOT NULL AND COALESCE(i.item->>'type', 'message') = 'message')) FROM fields f
LEFT JOIN LATERAL json_array_elements(f.replay_input) WITH ORDINALITY AS i(item, n) ON TRUE
GROUP BY f.id, f.turns, f."timestamp" ORDER BY f.id DESC`, "session tool entries", func(row rowScanner) error {
					var (
						raw   []byte
						entry harness.SessionEntry
					)
					if err := row.Scan(&summary.Turns, &raw); err != nil {
						return fmt.Errorf("scan session tool entry: %w", err)
					}

					if err := json.Unmarshal(raw, &entry); err != nil {
						return fmt.Errorf("decode session tool entry: %w", err)
					}

					summary.LastUpdated = cmp.Or(summary.LastUpdated, entry.Timestamp.Format(time.RFC3339))

					if !preview {
						return nil
					}

					messages, err := replayInputMessages(entry.ReplayInput)
					if err != nil {
						return err
					}

					for _, message := range slices.Backward(messages) {
						switch {
						case message.role == "user" && summary.LastUserMessage == "":
							summary.LastUserMessage = message.text
						case message.role == "assistant" && summary.LastAssistantMessage == "":
							summary.LastAssistantMessage = message.text
						}
					}

					return nil
				}, id, limit, preview)
				if err != nil {
					return err
				}

				if !preview || summary.Turns <= 1 || summary.LastUserMessage != "" && summary.LastAssistantMessage != "" {
					break
				}
			}

			return nil
		})
	}

	if err := group.Wait(); err != nil {
		return nil, fmt.Errorf("read session summaries: %w", err)
	}

	return summaries, nil
}

func currentSessionIDTool(conversationID string) harness.Tool {
	return harness.Tool{
		Name: sessionsCurrentIDToolName, Description: "Return only the owning RocketClaw bridge's bare stored conversation ID, without a JSON wrapper. Pass it as conversation_id to rocketclaw_sessions_get inside execute to read durable history, including entries before compaction. Child agents inherit this owning conversation ID, not a separate child-run ID. No arguments.",
		Permission: "rocketclaw", VisibilitySubjects: []string{sessionsSubject},
		Subjects:   func(json.RawMessage) ([]string, error) { return []string{sessionsSubject}, nil },
		Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false},
		Call: func(context.Context, json.RawMessage, chan<- harness.ChatResponse) (harness.ToolResult, error) {
			return harness.TextToolResult(conversationID), nil
		},
	}
}

func sessionsGetURLTool(threads *threadBridgeManager) harness.Tool {
	return harness.Tool{
		Name: sessionsGetURLToolName, Description: "Return only the clickable Web URL of a top-level session, one Web lists. Fails for Delegation Histories, private External MCP and cron producer conversations, and unknown IDs.",
		Permission: "rocketclaw", VisibilitySubjects: []string{sessionsSubject},
		Subjects:   func(json.RawMessage) ([]string, error) { return []string{sessionsSubject}, nil },
		Parameters: map[string]any{"properties": map[string]any{"session_id": map[string]any{"type": "string"}}, "required": []string{"session_id"}},
		Call: func(ctx context.Context, raw json.RawMessage, _ chan<- harness.ChatResponse) (harness.ToolResult, error) {
			var params struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(raw, &params); err != nil {
				return harness.ToolResult{}, fmt.Errorf("parse get session URL: %w", err)
			}

			if _, err := threads.store.checkTopLevelSession(ctx, params.SessionID); err != nil {
				return harness.ToolResult{}, err
			}

			url, err := threads.WebURL(params.SessionID)
			if err != nil {
				return harness.ToolResult{}, err
			}

			return harness.TextToolResult(url), nil
		},
	}
}

func sessionsMoveTool(service *SessionService, conversationID string) harness.Tool {
	return harness.Tool{
		Name: sessionsMoveToolName, Description: "Move this session to another Web space, named as the Web names it, ignoring case. It moves for everyone: the space becomes the session's Birth Space, which decides which people limited to some spaces can read it, what space: searches find, and where Restore puts its tabs. Tabs people already have stay where they are. Fails for a space that does not exist.",
		Permission: "rocketclaw", VisibilitySubjects: []string{sessionsSubject},
		Subjects:   func(json.RawMessage) ([]string, error) { return []string{sessionsSubject}, nil },
		Parameters: map[string]any{"type": "object", "properties": map[string]any{"space": map[string]any{"type": "string"}}, "required": []string{"space"}, "additionalProperties": false},
		Call: func(ctx context.Context, raw json.RawMessage, _ chan<- harness.ChatResponse) (harness.ToolResult, error) {
			var params struct {
				Space string `json:"space"`
			}
			if err := json.Unmarshal(raw, &params); err != nil {
				return harness.ToolResult{}, fmt.Errorf("parse move session: %w", err)
			}

			spaces, err := service.Spaces(ctx)
			if err != nil {
				return harness.ToolResult{}, err
			}

			i := slices.IndexFunc(spaces, func(space Space) bool { return strings.EqualFold(space.Name, params.Space) })
			if i < 0 {
				names := make([]string, len(spaces))
				for j := range spaces {
					names[j] = strconv.Quote(spaces[j].Name)
				}

				return harness.ToolResult{}, fmt.Errorf("no space is named %q; the spaces are %s", params.Space, strings.Join(names, ", "))
			}

			if err := service.MoveConversation(ctx, conversationID, spaces[i].ID); err != nil {
				return harness.ToolResult{}, err
			}

			return harness.TextToolResult("moved this session to " + spaces[i].Name), nil
		},
	}
}

type sessionWriteMode string

const (
	sessionWriteStop    sessionWriteMode = "stop"
	sessionWriteSteer   sessionWriteMode = "steer"
	sessionWriteEnqueue sessionWriteMode = "enqueue"
)

type sessionWriteOutcome string

const (
	sessionCreated sessionWriteOutcome = "created"
	sessionSteered sessionWriteOutcome = "steered"
	sessionQueued  sessionWriteOutcome = "queued"
	sessionStopped sessionWriteOutcome = "stopped"
)

const stopLineStep = "stopped_by/"

func (s *SessionService) parkStopLine(ctx context.Context, conversationID, key string, line json.RawMessage, reply *backgroundJob) error {
	tx, err := s.beginStateTx(ctx, "park stop line")
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(ctx, tx, conversationID); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO steps (conversation_id, key, value) VALUES ($1, $2, $3) ON CONFLICT (conversation_id, key) DO UPDATE SET value = excluded.value`, conversationID, key, string(removeSessionEntryNUL(line))); err != nil {
		return fmt.Errorf("park stop line: %w", err)
	}

	if reply != nil {
		if _, _, err := createBackgroundJob(ctx, tx, reply); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit stop line: %w", err)
	}

	return nil
}

func (s *SessionService) postStopLines(ctx context.Context, conversationID string) error {
	tx, err := s.beginStateTx(ctx, "post stop lines")
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(ctx, tx, conversationID); err != nil {
		return err
	}

	owners, err := queryStrings(ctx, tx, `WITH running AS (SELECT count(*) AS turns FROM turns WHERE conversation_id = $1 AND phase = $3 AND takes_lane),
parked AS (SELECT substr(key, length($2) + 1) AS input_id FROM steps, running WHERE conversation_id = $1 AND starts_with(key, $2) AND turns = 0),
ended AS (
    SELECT r.id, COALESCE(t.terminal, '') AS terminal FROM parked p JOIN turns r ON r.input_id = p.input_id LEFT JOIN turns t ON t.id = r.runner_id
    WHERE r.kind = 'reply' AND r.phase = 'running' AND r.reply_target = $1
), settled AS (
    UPDATE turns r SET phase = 'done', terminal = e.terminal, note = $4, wake = TRUE, updated_at_unix_ns = $5 FROM ended e WHERE r.id = e.id RETURNING r.conversation_id
)
SELECT DISTINCT conversation_id FROM settled`, "settle stop replies", conversationID, stopLineStep, turnRunning, notePending, timeUnixNano(time.Now()))
	if err != nil {
		return err
	}

	if _, err := appendQueriedEntries(ctx, tx, conversationID, "parked stop lines", `WITH running AS (SELECT count(*) AS turns FROM turns WHERE conversation_id = $1 AND phase = $3 AND takes_lane),
parked AS (DELETE FROM steps USING running WHERE conversation_id = $1 AND starts_with(key, $2) AND turns = 0 RETURNING value)
SELECT value FROM parked ORDER BY (value::jsonb->>'timestamp')::timestamptz`, conversationID, stopLineStep, turnRunning); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit stop lines: %w", err)
	}

	if len(owners) > 0 {
		s.jobs.turnFinished(owners)
	}

	return nil
}

func sessionsWriteTool(b *Bridge, origin, msg *protocol.InboundMessage, agentName, self string, agents harness.Agents) harness.Tool {
	owner := cmp.Or(origin.SyncDestination, self)
	properties := map[string]any{"session_id": map[string]any{"type": "string"}, "mode": map[string]any{"type": "string", "enum": []sessionWriteMode{sessionWriteStop, sessionWriteSteer, sessionWriteEnqueue}}}

	for _, field := range []string{"prompt", "title", "agent"} {
		properties[field] = map[string]any{"type": "string"}
	}

	return harness.Tool{Name: sessionsWriteToolName, Permission: "rocketclaw", VisibilitySubjects: []string{sessionsSubject}, Subjects: func(json.RawMessage) ([]string, error) { return []string{sessionsSubject}, nil },
		Description: "Create a top-level RocketClaw Web session, or write prompt to an existing one, without waiting for its turn. Supply every field. " +
			"With an empty session_id, create a session named title, run by agent (empty for this turn's agent), or reuse that agent's sticky session; mode is ignored and prompt is queued. A reused session keeps its name and history. The receiver cannot see the sending conversation, so prompt must carry all the context it needs. A personal sticky agent requires the originating person's login identity. " +
			"With session_id, deliver prompt to that session; title and agent are ignored. steer joins its running turn while that turn still takes input, otherwise runs as its next turn; enqueue runs after the work already queued; stop stops its current turn and goal, then runs prompt before anything already queued. " +
			"Only stop takes an empty prompt: it then just stops the turn and goal, queues nothing, and posts that this session stopped it into that session's transcript without starting a turn. " +
			"The receiver is told this session and agent sent it and that its answer comes back here. Each call that returns a queue_item_id gets exactly one reply: when the target's turn that took prompt ends, or, for an empty-prompt stop, once the stopped turn is over, a <session_reply> note naming that queue_item_id arrives here with the outcome and that turn's final answer. A steer's answer covers the whole turn it joined. " +
			"DO NOT wait, sleep, poll, or write again to get the reply: work on anything that does not depend on it, or end your response; you will be resumed when it arrives. " +
			"Every call, including create and stop, fails before doing anything when the target agent's permission.rocketclaw." + recipientSetting + ", or an easy target's delegatedFrom, does not accept this agent, except a write from a Slack channel's session into its own thread sessions. " +
			"Returns JSON with outcome (created, steered, queued or stopped), session_id, url, queue_item_id (absent only for an empty-prompt stop when no turn was running, which gets no reply) and, for stop, turn_was_running. Share url so a person can open the session.",
		Parameters: map[string]any{"properties": properties, "required": slices.Sorted(maps.Keys(properties))},
		Call: func(ctx context.Context, raw json.RawMessage, _ chan<- harness.ChatResponse) (harness.ToolResult, error) {
			var input struct {
				SessionID string           `json:"session_id"`
				Mode      sessionWriteMode `json:"mode"`
				Prompt    string           `json:"prompt"`
				Title     string           `json:"title"`
				Agent     string           `json:"agent"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return harness.ToolResult{}, fmt.Errorf("parse %s: %w", sessionsWriteToolName, err)
			}

			stopOnly := strings.TrimSpace(input.Prompt) == ""
			if stopOnly && (input.SessionID == "" || input.Mode != sessionWriteStop) {
				return harness.ToolResult{}, errors.New("prompt is required")
			}

			agent, _ := harness.ToolCallAgent(ctx)
			inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, input.Prompt, false)
			inbound.PreserveWhitespace = true

			inbound.Metadata = map[string]string{protocol.InboundOriginMetadataKey: "System", protocol.InboundMediaMetadataKey: "Text", protocol.InboundSenderSessionMetadataKey: owner, protocol.InboundSenderAgentMetadataKey: agent.Name}
			if username := origin.Metadata[protocol.InboundPrincipalEmailMetadataKey]; username != "" {
				inbound.Metadata[protocol.InboundPrincipalEmailMetadataKey] = username
			}

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

			result.QueueItemID = b.config.ConversationID + "/" + harness.ToolCallKey(ctx)
			reply := &backgroundJob{conversationID: b.config.ConversationID, jobID: result.QueueItemID, kind: turnReply, label: string(input.Mode), origin: msg}

			if input.SessionID == "" {
				title := strings.TrimSpace(input.Title)
				if title == "" {
					return harness.ToolResult{}, errors.New("title is required to create a session")
				}

				allowed := strings.FieldsFunc(origin.Metadata[protocol.InboundAllowedAgentsMetadataKey], func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' ' })
				start := threadStart{agent: cmp.Or(strings.TrimSpace(input.Agent), agentName), createdBy: ThreadCreator(origin.Metadata[protocol.InboundPrincipalEmailMetadataKey])}

				if origin.Cronjob != nil {
					allowed, start.createdBy = []string{agentName}, ThreadCreatedByCron
				}

				if err := checkRecipient(agents, start.agent, agent.Name); err != nil {
					return harness.ToolResult{}, err
				}

				result.Outcome, reply.agent, reply.label = sessionCreated, start.agent, "create"

				var resumed bool

				result.URL, resumed, err = b.threads.createSession(ctx, &start, allowed, title, inbound, reply)
				if resumed {
					result.Outcome = sessionQueued
				}

				result.SessionID = start.conversationID
			} else {
				var (
					targetAgent string
					ownThread   bool
				)

				if targetAgent, err = b.config.SessionService.checkSessionTarget(ctx, self, origin.SyncDestination, input.SessionID); err == nil {
					err = queryScalar(ctx, b.config.SessionService.db, "read own Thread Session", `SELECT count(*) > 0 FROM conversations s JOIN conversations t USING (kind, slack_channel, agent)
WHERE s.conversation_id = $1 AND t.conversation_id = $2 AND s.kind = 'slack_thread' AND s.slack_thread IS NULL AND t.slack_thread IS NOT NULL AND t.agent = $3`, &ownThread, self, input.SessionID, agent.Name)
				}

				if err == nil && !ownThread {
					err = checkRecipient(agents, targetAgent, agent.Name)
				}

				if err != nil {
					return harness.ToolResult{}, err
				}

				result.SessionID = input.SessionID
				if result.URL, err = b.threads.WebURL(input.SessionID); err != nil {
					return harness.ToolResult{}, err
				}

				reply.agent, reply.input, reply.target = targetAgent, result.URL, input.SessionID
				result.Outcome = sessionStopped

				if stopOnly {
					line, _ := json.Marshal(&harness.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now().UTC(), ReplayInput: replayInputForMessage("developer", fmt.Sprintf("Stopped by session %s (agent %s)", owner, agent.Name))})
					key := stopLineStep + result.QueueItemID

					if err = queryScalar(ctx, b.config.SessionService.db, "read stopped turn", `SELECT COALESCE(max(id), '') FROM turns WHERE conversation_id = $1 AND phase NOT IN ('waiting', 'done') AND takes_lane`, &reply.runnerID, input.SessionID); err == nil {
						running, err = b.threads.stopSession(input.SessionID)
					}

					if !running {
						reply, result.QueueItemID = nil, ""
					}

					if err == nil {
						err = b.config.SessionService.parkStopLine(ctx, input.SessionID, key, line, reply)
					}

					if err == nil {
						err = b.config.SessionService.postStopLines(ctx, input.SessionID)
					}
				} else {
					result.Outcome, running, err = b.threads.writeSession(ctx, input.SessionID, result.QueueItemID, input.Mode, inbound, reply)
				}

				if result.Outcome == sessionStopped {
					result.TurnWasRunning = &running
				}
			}

			if err != nil {
				return harness.ToolResult{}, err
			}

			data, _ := json.Marshal(result)

			return harness.TextToolResult(string(data)), nil
		}}
}

func storedEventLine(kind string, raw json.RawMessage) (role, text string) {
	if calls := harness.CodeModeCallsFromTrace([]json.RawMessage{raw}); len(calls) > 0 {
		return "code_mode", calls[0].String()
	}

	return "event", "[" + kind + ": stored event not rendered]"
}

func getSessionTool(service *SessionService) harness.Tool {
	return harness.Tool{
		Name: sessionsGetToolName, Description: fmt.Sprintf("Read the newest limit (1 to %d) stored entries of a durable conversation older than before_entry_id (0 for the newest) as timestamp, role and content TSV in stored entry order, including messages, readable reasoning and tool events before compaction. When older entries exist, the last line is [next_before_entry_id=N]; pass N to read the previous page. Store-wide access includes private MCP sessions. One snapshot, no polling or read-state changes. Use rocketclaw_sessions_current_id for your owning conversation or rocketclaw_sessions_list to find another ID.", maxSessionEntries),
		Permission: "rocketclaw", VisibilitySubjects: []string{sessionsSubject},
		Subjects: func(json.RawMessage) ([]string, error) { return []string{sessionsSubject}, nil },
		Parameters: map[string]any{"properties": map[string]any{
			"conversation_id": map[string]any{"type": "string"},
			"limit":           map[string]any{"type": "integer", "minimum": 1, "maximum": maxSessionEntries},
			"before_entry_id": map[string]any{"type": "integer", "minimum": 0},
		}, "required": []string{"conversation_id", "limit", "before_entry_id"}},
		Call: func(ctx context.Context, raw json.RawMessage, _ chan<- harness.ChatResponse) (harness.ToolResult, error) {
			var params struct {
				ConversationID string `json:"conversation_id"`
				Limit          int    `json:"limit"`
				BeforeEntryID  int64  `json:"before_entry_id"`
			}
			if err := json.Unmarshal(raw, &params); err != nil {
				return harness.ToolResult{}, fmt.Errorf("parse get session: %w", err)
			}

			params.ConversationID = strings.TrimSpace(params.ConversationID)
			if params.ConversationID == "" {
				return harness.ToolResult{}, errors.New("conversation ID is required")
			}

			if params.Limit < 1 || params.Limit > maxSessionEntries || params.BeforeEntryID < 0 {
				return harness.ToolResult{}, fmt.Errorf("limit must be between 1 and %d and before_entry_id must not be negative", maxSessionEntries)
			}

			observations, err := queryRows(ctx, service.db, `WITH `+sessionHistorySQL+`
SELECT id, entry_json, source_conversation_id, synced, revert_index FROM effective_entries
WHERE $2::bigint = 0 OR id < $2 ORDER BY id DESC LIMIT $3`, "session tool entries", scanObservedEntry, params.ConversationID, params.BeforeEntryID, params.Limit+1)
			if err != nil {
				return harness.ToolResult{}, err
			}

			older := len(observations) > params.Limit
			observations = observations[:min(len(observations), params.Limit)]

			var output strings.Builder

			escape := strings.NewReplacer("\\", "\\\\", "\t", "\\t", "\r", "\\r", "\n", "\\n")

			output.WriteString("timestamp\trole\tcontent\n")

			for i := range slices.Backward(observations) {
				entry := &observations[i].Entry

				timestamp := entry.Timestamp.Format(time.RFC3339Nano)
				for s, source := range [][]json.RawMessage{entry.ReplayInput, entry.OutputTrace} {
					for j, raw := range source {
						kind := harness.ReplayInputRawKind(raw)
						if kind == "" && s == 1 {
							kind = entry.Type
						}

						switch kind {
						case "", "message", "function_call", "function_call_output", "reasoning", "compaction":
						default:
							role, text := storedEventLine(kind, raw)
							writeSessionTranscriptRow(&output, escape, timestamp, role, text)

							continue
						}

						var (
							role, text string
							message    bool
						)

						items, err := harness.ReplayInputToParams([]json.RawMessage{raw})
						if err == nil {
							role, text, message, err = ReplayInputMessageRoleText(&items[0], raw)
						}

						if err != nil {
							if errReplay, ok := errors.AsType[*harness.ReplayDecodeError](err); ok {
								err = errReplay.Cause
							}

							writeSessionTranscriptRow(&output, escape, timestamp, "event", fmt.Sprintf("[entry %d %s item %d: stored event not decoded: %v]", observations[i].ID, [...]string{"replay_input", "output_trace"}[s], j, err))

							continue
						}

						item := &items[0]

						switch {
						case message:
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

						writeSessionTranscriptRow(&output, escape, timestamp, role, text)
					}
				}
			}

			if older {
				fmt.Fprintf(&output, "[next_before_entry_id=%d]\n", observations[len(observations)-1].ID)
			}

			return sessionToolComparisonResult(ctx, service.db, params.ConversationID, params.BeforeEntryID, &output, escape)
		},
	}
}

func writeSessionTranscriptRow(output *strings.Builder, escape *strings.Replacer, timestamp, role, text string) {
	role, text = escape.Replace(role), escape.Replace(text)
	output.Grow(len(timestamp) + len(role) + len(text) + 3)
	output.WriteString(timestamp)
	output.WriteByte('\t')
	output.WriteString(role)
	output.WriteByte('\t')
	output.WriteString(text)
	output.WriteByte('\n')
}

func sessionToolComparisonResult(ctx context.Context, db stateStoreDB, id string, before int64, output *strings.Builder, escape *strings.Replacer) (harness.ToolResult, error) {
	shadows, err := shadowHistoryDB(ctx, db, id, "", 0, before)
	if err != nil {
		return harness.ToolResult{}, err
	}

	if len(shadows) > 0 {
		output.WriteString("\nshadows\nparent_conversation_id\tparent_agent\tshadow_agent\tchild_conversation_id\n")

		for _, shadow := range shadows {
			fmt.Fprintf(output, "%s\t%s\t%s\t%s\n", escape.Replace(shadow.ParentConversationID), escape.Replace(shadow.ParentAgent), escape.Replace(shadow.ShadowAgent), escape.Replace(shadow.ChildConversationID))
		}
	}

	return harness.TextToolResult(output.String()), nil
}
