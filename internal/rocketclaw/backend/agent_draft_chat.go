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

const agentDraftMessageMetadataKey = "rocketclaw_agent_draft_message"

type AgentDraftMessage struct {
	InputID     string                `json:"-"`
	Text        string                `json:"text"`
	Labels      map[string]int64      `json:"labels"`
	Live        string                `json:"live,omitempty"`
	NotRun      []AgentDraftProblem   `json:"notRun,omitempty"`
	ExternalMCP AgentDraftExternalMCP `json:"externalMcp"`
}

type AgentDraftSend struct {
	ID, Username, Principal string
	Restart, Replay         bool
	Texts                   []string
}

type AgentDraftRefusedError struct {
	Problem  string
	Restart  bool
	Problems []AgentDraftProblem
}

func (e *AgentDraftRefusedError) Error() string { return e.Problem }

type agentDraftQueue interface {
	StashQueueItem(ctx context.Context, conversationID string, item *protocol.ThreadQueueItem) error
}

func (s *SessionService) SendAgentDraft(ctx context.Context, queue agentDraftQueue, send *AgentDraftSend) (string, []AgentDraftProblem, error) {
	tx, err := s.beginStateTx(ctx, "send agent draft")
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "agent-draft:"+send.ID); err != nil {
		return "", nil, fmt.Errorf("lock agent draft test chat: %w", err)
	}

	metadata := map[string]string{}

	draft, live, files, err := s.captureAgentDraft(ctx, send.ID, metadata)
	if err != nil {
		return "", nil, err
	}

	chat, mcp := draft.TestChat, draft.ExternalMCP

	texts, err := replayedAgentDraftTexts(ctx, tx, chat, send)
	if err != nil {
		return "", nil, err
	}

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

	if i := slices.IndexFunc(problems, func(problem AgentDraftProblem) bool { return problem.Variant == draft.Variants[0].Name }); i >= 0 {
		return draft.TestChat, nil, &AgentDraftRefusedError{Problem: "Fix the draft's settings before testing it.", Problems: problems[i : i+1 : i+1]}
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

	message, _ := json.Marshal(record)

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

	_ = json.Unmarshal(stored, &locked)

	if locked.On != mcp.On {
		return chat, nil, &AgentDraftRefusedError{Restart: true, Problem: map[bool]string{
			true:  "This test chat began in External MCP mode: restart it to test in the Web.",
			false: "This test chat began in the Web: restart it to test in External MCP mode.",
		}[locked.On]}
	}

	if key := changedAgentDraftKey(locked.Metadata, mcp.Metadata); key != "" {
		return chat, nil, &AgentDraftRefusedError{Restart: true, Problem: fmt.Sprintf("External MCP keeps %s at %q, the value this test chat's first message sent: restart the chat with the new metadata.", key, locked.Metadata[key])}
	}

	for _, problem := range problems {
		if problem.Variant == "" {
			delete(metadata, agentDraftLiveMetadataKey)
		} else {
			delete(files, path.Join(draft.Name, problem.Variant+".md"))
		}
	}

	data, _ := json.Marshal(files)
	metadata[agentDraftFilesMetadataKey] = string(data)

	if fresh {
		if err := s.startAgentDraftTestChat(ctx, draft, chat, send.Username); err != nil {
			return "", nil, err
		}
	}

	for i, text := range texts {
		inbound := &protocol.InboundMessage{Source: protocol.SourceWeb, Kind: protocol.InboundKindEnqueue, Text: text, Human: true, ConversationID: chat, Metadata: maps.Clone(metadata)}
		id := rand.Text()

		if mcp.On {
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
			if i > 0 {
				err = errors.Join(err, tx.Commit())
			}

			return "", nil, err
		}

		record.Text = text
		recorded, _ := json.Marshal(record)

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

func replayedAgentDraftTexts(ctx context.Context, tx stateStoreDB, chat string, send *AgentDraftSend) ([]string, error) {
	if !send.Restart || !send.Replay {
		return send.Texts, nil
	}

	earlier, err := queryStrings(ctx, tx, `SELECT message->>'text' FROM agent_draft_messages WHERE conversation_id = $1 ORDER BY seq`, "agent draft replay", chat)

	return append(earlier, send.Texts...), err
}

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

func (s *SessionService) agentDraftRecord(ctx context.Context, draft *AgentDraft, live *string, problems []AgentDraftProblem) (AgentDraftMessage, error) {
	record := AgentDraftMessage{Labels: map[string]int64{}, NotRun: problems}
	if draft.ExternalMCP.On {
		record.ExternalMCP = draft.ExternalMCP
	}

	if live != nil {
		record.Live = fmt.Sprintf("%x", sha256.Sum256([]byte(*live)))[:12]
	}

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

func agentDraftStickyOff(settings []AgentDraftSetting) bool {
	var sticky string

	i := slices.IndexFunc(settings, func(setting AgentDraftSetting) bool { return setting.Key == "sticky" })
	if i >= 0 {
		_ = yaml.Unmarshal([]byte(settings[i].Value), &sticky)
	}

	return i < 0 || sticky == "off"
}
