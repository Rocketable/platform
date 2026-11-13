package backend

import (
	"context"
	"crypto/rand"
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

// agentDraftFirstStep keys the step of a draft test chat that holds its first message's External MCP mode
// and pairs, which the chat keeps for good, as real External MCP keeps a thread's first keys.
const agentDraftFirstStep = "agent-draft-first-message"

// agentDraftNotRunMetadataKey holds, on a draft test message, the JSON problems of the variants and live
// agent that do not answer it.
const agentDraftNotRunMetadataKey = "rocketclaw_agent_draft_not_run"

// AgentDraftSend is what an editor sends to a draft's test chat: Texts, in order, from Username, shown as
// Principal, into a new test chat when Restart is set.
type AgentDraftSend struct {
	ID, Username, Principal string
	Restart                 bool
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
// stored. It first creates the chat when the draft has none or send restarts it; an earlier chat keeps its
// messages and the answers still running in it. One capture of the draft and the live agent serves every
// text, and every check runs on it. In External MCP mode each text is stored as a real External MCP call's
// message, and a sticky agent, a change of mode, or a changed or removed key of the chat's first message
// refuses it. A variant or live file that does not load, or a live agent External MCP would not serve, does
// not answer; SendAgentDraft returns the chat and those problems.
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
	if chat == "" || send.Restart {
		chat = rand.Text()
		if _, err := s.recordConversation(ctx, &protocol.Conversation{ID: chat, Agent: draft.Name, CreatedBy: send.Username}, conversationFacts{kind: KindAgentDraft}, harness.AgentStickyOff); err != nil {
			return "", nil, err
		}

		if _, err := execState(ctx, s.db, "bind agent draft test chat", `UPDATE conversations SET agent_draft_id = $2 WHERE conversation_id = $1`, chat, draft.ID); err != nil {
			return "", nil, err
		}

		if _, err := s.ApplyAgentDraftOp(ctx, draft.ID, &AgentDraftOp{BaseRevision: draft.Revision, OpID: rand.Text(), Author: send.Username, Kind: AgentDraftSetTestChat, Value: chat}); err != nil {
			return "", nil, err
		}
	}

	if len(send.Texts) == 0 {
		return chat, nil, nil
	}

	problems, err := s.ValidateAgentDraft(draft)
	if err != nil {
		return "", nil, err
	}

	if own := slices.DeleteFunc(slices.Clone(problems), func(problem AgentDraftProblem) bool { return problem.Variant != draft.Variants[0].Name }); len(own) > 0 {
		return chat, nil, &AgentDraftRefusedError{Problem: "Fix the draft's settings before testing it.", Problems: own}
	}

	if mcp.On && slices.ContainsFunc(draft.Variants, func(variant AgentDraftVariant) bool { return !agentDraftStickyOff(variant.Settings) }) {
		return chat, nil, &AgentDraftRefusedError{Problem: "External MCP only serves sticky: off agents: set sticky to off in every variant, or turn External MCP mode off."}
	}

	liveProblems, err := s.liveAgentDraftProblems(draft.Name, live, mcp.On)
	if err != nil {
		return "", nil, err
	}

	problems = append(problems, liveProblems...)

	// The chat's first message fixes its mode and, in External MCP mode, its keys.
	record := AgentDraftExternalMCP{On: mcp.On}
	if mcp.On {
		record.Metadata = mcp.Metadata
	}

	first, _ := json.Marshal(record) // Strings always encode.

	var stored []byte
	if err := tx.QueryRowContext(ctx, `WITH saved AS (
    INSERT INTO steps (conversation_id, key, value) VALUES ($1, $2, $3) ON CONFLICT (conversation_id, key) DO NOTHING RETURNING value
)
SELECT value FROM saved UNION ALL SELECT value FROM steps WHERE conversation_id = $1 AND key = $2`, chat, agentDraftFirstStep, string(first)).Scan(&stored); err != nil {
		return "", nil, fmt.Errorf("read agent draft test chat's first message: %w", err)
	}

	var locked AgentDraftExternalMCP

	_ = json.Unmarshal(stored, &locked) // Stored above, by this code.

	if locked.On != mcp.On {
		return chat, nil, &AgentDraftRefusedError{Restart: true, Problem: map[bool]string{
			true:  "This test chat began in External MCP mode: restart it to test in the Web.",
			false: "This test chat began in the Web: restart it to test in External MCP mode.",
		}[locked.On]}
	}

	for _, key := range slices.Sorted(maps.Keys(locked.Metadata)) {
		if value, ok := mcp.Metadata[key]; !ok || value != locked.Metadata[key] {
			return chat, nil, &AgentDraftRefusedError{Restart: true, Problem: fmt.Sprintf("External MCP keeps %s at %q, the value this test chat's first message sent: restart the chat with the new metadata.", key, locked.Metadata[key])}
		}
	}

	// A variant or live file that does not load, or a live agent External MCP would not serve, gets no message.
	var files map[string]string

	_ = json.Unmarshal([]byte(captured.Metadata[agentDraftFilesMetadataKey]), &files) // CaptureAgentDraft encoded it.

	for _, problem := range problems {
		if problem.Variant == "" {
			delete(captured.Metadata, agentDraftLiveMetadataKey)
		} else {
			delete(files, path.Join(draft.Name, problem.Variant+".md"))
		}
	}

	data, _ := json.Marshal(files) // Strings always encode.
	captured.Metadata[agentDraftFilesMetadataKey] = string(data)

	if len(problems) > 0 {
		data, _ = json.Marshal(problems) // Strings always encode.
		captured.Metadata[agentDraftNotRunMetadataKey] = string(data)
	}

	for i, text := range send.Texts {
		// Each message is its own turn, never a steer that joins a running one.
		inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindEnqueue, &protocol.InboundContent{Text: text}, true)
		inbound.ConversationID, inbound.Metadata = chat, maps.Clone(captured.Metadata)
		id := rand.Text()

		if mcp.On {
			// As submitExternalMCPInput builds a real call's message: the pairs only become variables, and the
			// caller is the principal.
			inbound.Source, inbound.Kind, inbound.ExternalMCPMetadata = protocol.SourceExternalMCP, protocol.InboundKindPrompt, map[string]string{"external_conversation_id": chat}
			maps.Copy(inbound.ExternalMCPMetadata, mcp.Metadata)

			if mcp.Caller != "" {
				inbound.Metadata[protocol.InboundPrincipalMetadataKey] = mcp.Caller
			}
		} else {
			inbound.Metadata[protocol.InboundPrincipalMetadataKey], inbound.Metadata[protocol.InboundPrincipalNameMetadataKey], inbound.Metadata[protocol.InboundPrincipalEmailMetadataKey] = send.Principal, send.Principal, send.Username
			inbound.Metadata["web_message_id"] = id
		}

		item := &protocol.ThreadQueueItem{ID: id, Message: text, Principal: inbound.Metadata[protocol.InboundPrincipalMetadataKey], Source: inbound.Source, Kind: protocol.InboundKindEnqueue, StashAt: time.Now().UTC(), Inbound: inbound}
		if err := queue.StashQueueItem(ctx, chat, item); err != nil {
			// Messages already stored keep the first message's record.
			if i > 0 {
				err = errors.Join(err, tx.Commit())
			}

			return "", nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return "", nil, fmt.Errorf("commit agent draft test messages: %w", err)
	}

	return chat, problems, nil
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
