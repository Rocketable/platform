package backend

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"gopkg.in/yaml.v3"
)

// agentDraftMessageMetadataKey gives an External MCP draft test message's input the id of its queue item, as
// web_message_id does a Web message's, so History shows it by the id its record is stored under.
const agentDraftMessageMetadataKey = "rocketclaw_agent_draft_message"

// AgentDraftMessage is what a message to a draft's test chat was sent with, by InputID, the input id History
// shows for it: Text is the message as sent; Labels gives each column, by variant name, the revision of the
// last change to its file; Live is the live file's version, empty for none; NotRun lists the columns that did
// not answer it, the live one without a variant; ExternalMCP is its mode, pairs and caller. A chat's first
// message fixes its mode and keys, as real External MCP keeps a thread's first keys.
type AgentDraftMessage struct {
	InputID     string                `json:"-"`
	Text        string                `json:"text"`
	Labels      map[string]int64      `json:"labels"`
	Live        string                `json:"live,omitempty"`
	NotRun      []AgentDraftProblem   `json:"notRun,omitempty"`
	ExternalMCP AgentDraftExternalMCP `json:"externalMcp"`
}

// AgentDraftSend is what an editor sends to a draft's test chat: Texts, in order, from Username, shown as
// Principal, into a new test chat when Restart is set. Replay, with Restart, sends the old chat's messages
// first, in the order they were stored.
type AgentDraftSend struct {
	ID, Username, Principal string
	Restart, Replay         bool
	Texts                   []string
}

// AgentDraftRefusedError refuses a draft test message before it is stored. Restart says a new test chat
// would take it; Problems are the draft's own settings that keep it from loading.
type AgentDraftRefusedError struct {
	Problem  string
	Restart  bool
	Problems []AgentDraftProblem
}

func (e *AgentDraftRefusedError) Error() string { return e.Problem }

// agentDraftQueue stores a message in its conversation's queue, as Runtime.StashQueueItem does.
type agentDraftQueue interface {
	StashQueueItem(ctx context.Context, conversationID string, item *protocol.ThreadQueueItem) error
}

// SendAgentDraft stores send's texts in draft send.ID's test chat through queue, and returns once they are
// stored. It creates the chat when the draft has none or send restarts it, once the texts pass every check; an
// earlier chat keeps its messages and the answers still running in it, and a replay sends them again first. One capture of the draft and the live agent serves every
// text, and every check runs on it. In External MCP mode each text is stored as a real External MCP call's
// message, and a sticky agent, two keys of one variable, a key the conversation id overwrites, a change of
// mode, or a changed or removed key of the chat's first message refuses it. A variant or live file that does
// not load, or a live agent External MCP would not serve, does not answer; SendAgentDraft returns the chat and
// those problems. Each stored text keeps its record.
func (s *SessionService) SendAgentDraft(ctx context.Context, queue agentDraftQueue, send *AgentDraftSend) (string, []AgentDraftProblem, error) {
	tx, err := s.beginStateTx(ctx, "send agent draft")
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// One send at a time per draft: the first message stored is the first a column answers, and no send
	// checks pairs a later capture replaced.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "agent-draft:"+send.ID); err != nil {
		return "", nil, fmt.Errorf("lock agent draft test chat: %w", err)
	}

	captured := &protocol.InboundMessage{Metadata: map[string]string{}}

	draft, live, err := s.CaptureAgentDraft(ctx, send.ID, captured)
	if err != nil {
		return "", nil, err
	}

	chat, mcp := draft.TestChat, draft.ExternalMCP

	texts, err := replayedAgentDraftTexts(ctx, tx, chat, send)
	if err != nil {
		return "", nil, err
	}

	// A new chat is made only once its messages pass every check, so a refused restart leaves the draft on its
	// chat and its messages.
	fresh := chat == "" || send.Restart
	if fresh {
		chat = rand.Text()
	}

	if len(texts) == 0 {
		if fresh {
			return chat, nil, s.startAgentDraftTestChat(ctx, draft, chat, send.Username)
		}

		return chat, nil, nil
	}

	problems, err := s.ValidateAgentDraft(draft)
	if err != nil {
		return "", nil, err
	}

	if own := slices.DeleteFunc(slices.Clone(problems), func(problem AgentDraftProblem) bool { return problem.Variant != draft.Variants[0].Name }); len(own) > 0 {
		return draft.TestChat, nil, &AgentDraftRefusedError{Problem: "Fix the draft's settings before testing it.", Problems: own}
	}

	if mcp.On && slices.ContainsFunc(draft.Variants, func(variant AgentDraftVariant) bool { return !agentDraftStickyOff(variant.Settings) }) {
		return draft.TestChat, nil, &AgentDraftRefusedError{Problem: "External MCP only serves sticky: off agents: set sticky to off in every variant, or turn External MCP mode off."}
	}

	if problem := agentDraftPairsProblem(mcp); problem != "" {
		return draft.TestChat, nil, &AgentDraftRefusedError{Problem: problem}
	}

	liveProblems, err := s.liveAgentDraftProblems(draft.Name, live, mcp.On)
	if err != nil {
		return "", nil, err
	}

	problems = append(problems, liveProblems...)

	record, err := s.agentDraftRecord(ctx, draft, live, problems)
	if err != nil {
		return "", nil, err
	}

	// The chat's first message fixes its mode and, in External MCP mode, its keys.
	message, _ := json.Marshal(record) // Strings and numbers always encode.

	var (
		stored []byte
		locked AgentDraftExternalMCP
	)

	if err := tx.QueryRowContext(ctx, `WITH first AS (
    SELECT message->'externalMcp' AS mcp FROM agent_draft_messages WHERE conversation_id = $1 ORDER BY seq LIMIT 1
), this AS (SELECT $2::jsonb->'externalMcp' AS mcp)
SELECT COALESCE(f.mcp, t.mcp) FROM this t LEFT JOIN first f ON true`, chat, string(message)).Scan(&stored); err != nil {
		return "", nil, fmt.Errorf("read agent draft test chat's first message: %w", err)
	}

	_ = json.Unmarshal(stored, &locked) // Encoded by this code.

	if locked.On != mcp.On {
		return chat, nil, &AgentDraftRefusedError{Restart: true, Problem: map[bool]string{
			true:  "This test chat began in External MCP mode: restart it to test in the Web.",
			false: "This test chat began in the Web: restart it to test in External MCP mode.",
		}[locked.On]}
	}

	if key := changedAgentDraftKey(locked.Metadata, mcp.Metadata); key != "" {
		return chat, nil, &AgentDraftRefusedError{Restart: true, Problem: fmt.Sprintf("External MCP keeps %s at %q, the value this test chat's first message sent: restart the chat with the new metadata.", key, locked.Metadata[key])}
	}

	// A variant or live file that does not load, or a live agent External MCP would not serve, gets no message.
	files, _ := agentDraftFiles(captured) // CaptureAgentDraft encoded them.

	for _, problem := range problems {
		if problem.Variant == "" {
			delete(captured.Metadata, agentDraftLiveMetadataKey)
		} else {
			delete(files, path.Join(draft.Name, problem.Variant+".md"))
		}
	}

	data, _ := json.Marshal(files) // Strings always encode.
	captured.Metadata[agentDraftFilesMetadataKey] = string(data)

	if fresh {
		if err := s.startAgentDraftTestChat(ctx, draft, chat, send.Username); err != nil {
			return "", nil, err
		}
	}

	for i, text := range texts {
		// Each message is its own turn, never a steer that joins a running one.
		inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindEnqueue, &protocol.InboundContent{Text: text}, true)
		inbound.ConversationID, inbound.Metadata = chat, maps.Clone(captured.Metadata)
		id := rand.Text()

		if mcp.On {
			// As submitExternalMCPInput builds a real call's message: the pairs only become variables, and the
			// caller is the principal.
			inbound.Source, inbound.Kind, inbound.ExternalMCPMetadata = protocol.SourceExternalMCP, protocol.InboundKindPrompt, map[string]string{}
			inbound.Metadata[agentDraftMessageMetadataKey] = id
			maps.Copy(inbound.ExternalMCPMetadata, mcp.Metadata)
			inbound.ExternalMCPMetadata["external_conversation_id"] = chat

			if mcp.Caller != "" {
				inbound.Metadata[protocol.InboundPrincipalMetadataKey] = mcp.Caller
			}
		} else {
			inbound.Metadata[protocol.InboundPrincipalMetadataKey], inbound.Metadata[protocol.InboundPrincipalNameMetadataKey], inbound.Metadata[protocol.InboundPrincipalEmailMetadataKey] = send.Principal, send.Principal, send.Username
			inbound.Metadata["web_message_id"] = id
		}

		item := &protocol.ThreadQueueItem{ID: id, Message: text, Principal: inbound.Metadata[protocol.InboundPrincipalMetadataKey], Source: inbound.Source, Kind: protocol.InboundKindEnqueue, StashAt: time.Now().UTC(), Inbound: inbound}
		if err := queue.StashQueueItem(ctx, chat, item); err != nil {
			// Messages already stored keep their records.
			if i > 0 {
				err = errors.Join(err, tx.Commit())
			}

			return "", nil, err
		}

		record.Text = text
		recorded, _ := json.Marshal(record) // Strings and numbers always encode.

		// Teammates' pages read the record once it commits.
		if _, err := execState(ctx, tx, "record agent draft test message", `WITH saved AS (
    INSERT INTO agent_draft_messages (conversation_id, input_id, message) VALUES ($1, $2, $3) RETURNING $4::text AS draft_id
)
SELECT `+notifyAgentDraft+` FROM saved`,
			chat, id, string(recorded), draft.ID); err != nil {
			return "", nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return "", nil, fmt.Errorf("commit agent draft test messages: %w", err)
	}

	return chat, problems, nil
}

// startAgentDraftTestChat makes chat, started by username, draft's test chat.
func (s *SessionService) startAgentDraftTestChat(ctx context.Context, draft *AgentDraft, chat, username string) error {
	if _, err := s.recordConversation(ctx, &protocol.Conversation{ID: chat, Agent: draft.Name, CreatedBy: username}, conversationFacts{kind: KindAgentDraft}, harness.AgentStickyOff); err != nil {
		return err
	}

	if _, err := execState(ctx, s.db, "bind agent draft test chat", `UPDATE conversations SET agent_draft_id = $2 WHERE conversation_id = $1`, chat, draft.ID); err != nil {
		return err
	}

	_, err := s.ApplyAgentDraftOp(ctx, draft.ID, &AgentDraftOp{BaseRevision: draft.Revision, OpID: rand.Text(), Author: username, Kind: AgentDraftSetTestChat, Value: chat})

	return err
}

// replayedAgentDraftTexts returns send's texts, after the messages of chat, the old test chat, in the order they
// were stored, when send restarts with a replay. tx holds the draft's send lock, so a teammate's message stored before it is replayed, and one
// stored after it goes to the new chat.
func replayedAgentDraftTexts(ctx context.Context, tx stateStoreDB, chat string, send *AgentDraftSend) ([]string, error) {
	if !send.Restart || !send.Replay {
		return send.Texts, nil
	}

	earlier, err := queryStrings(ctx, tx, `SELECT message->>'text' FROM agent_draft_messages WHERE conversation_id = $1 ORDER BY seq`, "agent draft replay", chat)

	return append(earlier, send.Texts...), err
}

// agentDraftPairsProblem returns why External MCP mode mcp cannot send its pairs, "" when it can. The message
// shows every key, so two keys of one variable, of which the agent gets one value, and a key the conversation
// id overwrites are refused.
func agentDraftPairsProblem(mcp AgentDraftExternalMCP) string {
	if !mcp.On {
		return ""
	}

	keys := map[string]string{}

	for _, key := range slices.Sorted(maps.Keys(mcp.Metadata)) {
		variable := externalMCPMetadataVariable(key)
		other, clash := keys[variable]

		switch {
		case variable == externalMCPMetadataVariable("external_conversation_id"):
			return fmt.Sprintf("External MCP sets %s to the conversation's id: remove the metadata key %s.", variable, key)
		case clash:
			return fmt.Sprintf("Metadata keys %s and %s both become %s: keep one of them.", other, key, variable)
		}

		keys[variable] = key
	}

	return ""
}

// changedAgentDraftKey returns the first of the locked keys whose variable sent leaves out or gives another
// value, "" when none does. Keys are compared as the variables they become, so a-b and a_b are one key.
func changedAgentDraftKey(locked, sent map[string]string) string {
	for _, key := range slices.Sorted(maps.Keys(locked)) {
		kept := false

		for name, value := range sent {
			if externalMCPMetadataVariable(name) == externalMCPMetadataVariable(key) {
				if kept = value == locked[key]; !kept {
					break
				}
			}
		}

		if !kept {
			return key
		}
	}

	return ""
}

// agentDraftRecord returns what a message to draft's test chat is sent with when draft and the live file live,
// nil for none, are its capture and problems keep columns from answering it. A column's label is the oldest
// revision, back from draft's, whose file for that column is the same.
func (s *SessionService) agentDraftRecord(ctx context.Context, draft *AgentDraft, live *string, problems []AgentDraftProblem) (AgentDraftMessage, error) {
	record := AgentDraftMessage{Labels: map[string]int64{}, NotRun: problems}
	if draft.ExternalMCP.On {
		record.ExternalMCP = draft.ExternalMCP
	}

	if live != nil {
		record.Live = fmt.Sprintf("%x", sha256.Sum256([]byte(*live)))[:12]
	}

	// ponytail: each send reads every revision of the draft; a label kept on agent_draft_ops would lift this.
	revisions, err := queryRows(ctx, s.db, `SELECT draft_id, revision, draft FROM agent_draft_ops WHERE draft_id = $1 AND revision <= $2 ORDER BY revision DESC`, "agent draft revisions", scanAgentDraft, draft.ID, draft.Revision)
	if err != nil {
		return AgentDraftMessage{}, err
	}

	for _, variant := range draft.Variants {
		file := renderAgentDraftFile(draft.Mode, &variant)

		for j := range revisions {
			old := &revisions[j]

			i := old.variant(variant.Name)
			if i < 0 || !bytes.Equal(renderAgentDraftFile(old.Mode, &old.Variants[i]), file) {
				break
			}

			record.Labels[variant.Name] = old.Revision
		}
	}

	return record, nil
}

// AgentDraftMessages returns what each message to the test chat was sent with, in the order sent.
func (s *SessionService) AgentDraftMessages(ctx context.Context, chat string) ([]AgentDraftMessage, error) {
	return queryRows(ctx, s.db, `SELECT input_id, message FROM agent_draft_messages WHERE conversation_id = $1 ORDER BY seq`, "agent draft messages", func(row rowScanner) (AgentDraftMessage, error) {
		var (
			message AgentDraftMessage
			raw     []byte
		)

		err := row.Scan(&message.InputID, &raw)
		if err == nil {
			err = json.Unmarshal(raw, &message)
		}

		if err != nil {
			return message, fmt.Errorf("read agent draft message: %w", err)
		}

		return message, nil
	}, chat)
}

// liveAgentDraftProblems returns why the live agent name, whose effective file is live (nil for none), does not
// answer a test message, each without a variant: its file does not load, or External MCP mode (mcp) would
// not serve it.
func (s *SessionService) liveAgentDraftProblems(name string, live *string, mcp bool) ([]AgentDraftProblem, error) {
	if live == nil {
		return nil, nil
	}

	mode, variant, err := parseAgentDraftFile(*live)
	if err != nil {
		return []AgentDraftProblem{{Sentence: fmt.Sprintf("The live agent's file isn't valid: %v.", err)}}, nil
	}

	problems, err := s.ValidateAgentDraft(&AgentDraft{Name: name, Mode: mode, Variants: []AgentDraftVariant{variant}})
	if mcp && !agentDraftStickyOff(variant.Settings) {
		problems = append(problems, AgentDraftProblem{Key: "sticky", Sentence: "External MCP only serves sticky: off agents, and the live agent is not."})
	}

	return problems, err
}

// agentDraftStickyOff reports whether settings leave sticky off, as the agent loader reads it.
func agentDraftStickyOff(settings []AgentDraftSetting) bool {
	var sticky string

	i := slices.IndexFunc(settings, func(setting AgentDraftSetting) bool { return setting.Key == "sticky" })
	if i >= 0 {
		_ = yaml.Unmarshal([]byte(settings[i].Value), &sticky) // A setting's YAML parsed when it was written.
	}

	return i < 0 || sticky == "off"
}
