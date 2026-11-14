package backend

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/require"
)

// draftChatTest runs a draft whose variant a answers as COL-A and b as COL-B. A column asked a message with a
// gate waits for the gate to close; held gets each such column's answer before it waits.
type draftChatTest struct {
	dt      *agentDraftTest
	answers chan [2]string
	held    chan string
	gates   sync.Map
	order   []string
}

func startDraftChatTest(t *testing.T, name string, agents map[string]string) *draftChatTest {
	t.Helper()

	ct := &draftChatTest{answers: make(chan [2]string, 64), held: make(chan string, 64)}
	ct.dt = startAgentDraftTest(t, agents, func(w http.ResponseWriter, column, user, body string) {
		message := regexp.MustCompile(`MSG-\w+`).FindString(user)
		answer := column + "/" + message

		if gate, ok := ct.gates.Load(message); ok {
			ct.held <- answer

			<-gate.(chan struct{})
		}

		writeRawRunMessage(t, w, "resp", "msg", answer)

		ct.answers <- [2]string{answer, strings.ReplaceAll(body, `\"`, `"`)}
	})

	// Runs before the provider closes, so no held request outlives the test.
	t.Cleanup(func() {
		ct.gates.Range(func(_, gate any) bool {
			select {
			case <-gate.(chan struct{}):
			default:
				close(gate.(chan struct{}))
			}

			return true
		})
	})

	draft, err := ct.dt.rt.Sessions.CreateAgentDraft(t.Context(), "alice", AgentDraftOrigin{Name: name, Mode: harness.AgentModeAdvanced})
	require.NoError(t, err)

	ct.dt.draft, ct.dt.revision = draft, draft.Revision
	ct.dt.edit(t, &AgentDraftOp{Kind: AgentDraftEditFile, Variant: "a", Value: "---\nmodel: gpt-6.1-sol\n---\nCOL-A\n"})
	ct.dt.edit(t, &AgentDraftOp{Kind: AgentDraftAddVariant, Variant: "b", Value: "a"})
	ct.dt.edit(t, &AgentDraftOp{Kind: AgentDraftEditFile, Variant: "b", Value: "---\nmodel: gpt-6.1-sol\n---\nCOL-B\n"})

	return ct
}

func (ct *draftChatTest) send(t *testing.T, restart bool, texts ...string) (string, []AgentDraftProblem, error) {
	t.Helper()

	return ct.dt.rt.Sessions.SendAgentDraft(t.Context(), ct.dt.rt, &AgentDraftSend{ID: ct.dt.draft.ID, Username: "alice@example.com", Principal: "Alice", Restart: restart, Texts: texts})
}

// refused sends text and returns the refusal, checking nothing was stored.
func (ct *draftChatTest) refused(t *testing.T, text string) *AgentDraftRefusedError {
	t.Helper()

	chat, _, err := ct.send(t, false, text)
	refusal, ok := errors.AsType[*AgentDraftRefusedError](err)
	require.True(t, ok, "%v", err)
	require.Zero(t, ct.count(t, `SELECT count(*) FROM turns WHERE conversation_id = $1 AND inbound_json->>'Text' = $2`, chat, text))

	return refusal
}

// hold makes the columns asked message wait until the returned gate closes.
func (ct *draftChatTest) hold(message string) chan struct{} {
	gate := make(chan struct{})
	ct.gates.Store(message, gate)

	return gate
}

// waitHeld waits until a column asked message holds its answer.
func (ct *draftChatTest) waitHeld(t *testing.T, message string) {
	t.Helper()

	for answer := range ct.held {
		if strings.HasSuffix(answer, "/"+message) {
			return
		}
	}
}

// stored sends text, failing unless the send returns within a minute.
func (ct *draftChatTest) stored(t *testing.T, text string) {
	t.Helper()

	done := make(chan error, 1)

	go func() {
		_, _, err := ct.send(t, false, text)
		done <- err
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Minute):
		t.Fatalf("sending %s waited for the running turn", text)
	}
}

// collect checks the next answers are want, and returns each with the request that asked for it; order gets
// every answer in the order it came.
func (ct *draftChatTest) collect(t *testing.T, want ...string) map[string]string {
	t.Helper()

	got, deadline := map[string]string{}, time.After(time.Minute)
	for len(got) < len(want) {
		select {
		case answer := <-ct.answers:
			got[answer[0]] = answer[1]
			ct.order = append(ct.order, answer[0])
		case <-deadline:
			t.Fatalf("got only %d answers of %q", len(got), want)
		}
	}

	keys := make([]string, 0, len(got))
	for answer := range got {
		keys = append(keys, answer)
	}

	require.ElementsMatch(t, want, keys)

	return got
}

// messages returns what chat's messages were sent with, checking each is known by the input id History shows
// for its user message, in order.
func (ct *draftChatTest) messages(t *testing.T, chat string) []AgentDraftMessage {
	t.Helper()

	messages, err := ct.dt.rt.Sessions.AgentDraftMessages(t.Context(), chat)
	require.NoError(t, err)

	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.InputID)
	}

	var shown []string

	require.Eventually(t, func() bool {
		shown, err = queryStrings(t.Context(), ct.dt.rt.Sessions.db, `SELECT item->>'input_id' FROM entries e
CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry_json::jsonb->'replay_input', 'null'::jsonb)) item
WHERE e.conversation_id = $1 AND item->>'role' = 'user' ORDER BY e.id`, "user input ids", chat)

		return err == nil && len(shown) >= len(ids)
	}, time.Minute, 10*time.Millisecond)
	require.Equal(t, ids, shown)

	return messages
}

func (ct *draftChatTest) count(t *testing.T, query string, args ...any) int {
	t.Helper()

	var n int
	require.NoError(t, ct.dt.rt.Sessions.db.QueryRowContext(t.Context(), query, args...).Scan(&n))

	return n
}

// A Web test message is each column's own turn from the person who sent it, stored at once even while a turn
// runs. The draft's own invalid setting refuses it; a variant's keeps only that variant from answering. A
// variant removed and added back answers only the messages it was sent with. External MCP
// mode cannot start mid-chat. A restart leaves running answers in the old chat, and a replay sends the old
// messages in order. Test chats are never listed.
func TestAgentDraftChatWebMessages(t *testing.T) {
	t.Parallel()

	ct := startDraftChatTest(t, "helper", nil)
	dt := ct.dt

	dt.edit(t, &AgentDraftOp{Kind: AgentDraftSetField, Variant: "a", Key: "sticky", Value: "maybe"})
	refusal := ct.refused(t, "MSG-0")
	require.Equal(t, []AgentDraftProblem{{Variant: "a", Key: "sticky", Sentence: "off, user or global."}}, refusal.Problems)
	require.False(t, refusal.Restart)

	dt.edit(t, &AgentDraftOp{Kind: AgentDraftSetField, Variant: "a", Key: "sticky"})
	revA := dt.revision
	dt.edit(t, &AgentDraftOp{Kind: AgentDraftSetField, Variant: "b", Key: "sticky", Value: "maybe"})
	revB := dt.revision
	chat, problems, err := ct.send(t, false, "MSG-1")
	require.NoError(t, err)
	require.Equal(t, []AgentDraftProblem{{Variant: "b", Key: "sticky", Sentence: "off, user or global."}}, problems)

	request := ct.collect(t, "COL-A/MSG-1")["COL-A/MSG-1"]
	require.Contains(t, request, `[Web principal="Alice"`)
	require.Contains(t, request, askUserQuestionToolName+"(", "a person's Web message may be asked back")

	dt.edit(t, &AgentDraftOp{Kind: AgentDraftSetField, Variant: "b", Key: "sticky"})

	gate := ct.hold("MSG-HOLD1")
	_, _, err = ct.send(t, false, "MSG-HOLD1")
	require.NoError(t, err)
	ct.waitHeld(t, "MSG-HOLD1")
	ct.stored(t, "MSG-2")
	close(gate)
	ct.collect(t, "COL-A/MSG-HOLD1", "COL-B/MSG-HOLD1", "COL-A/MSG-2", "COL-B/MSG-2")

	// Only b's file changed between the messages, back to what it was: a keeps its label and b's moves on.
	// Each message keeps the columns that did not answer it; with no live agent there is no live version.
	messages := ct.messages(t, chat)
	require.Len(t, messages, 3)
	require.Equal(t, AgentDraftMessage{InputID: messages[0].InputID, Text: "MSG-1", Labels: map[string]int64{"a": revA, "b": revB},
		NotRun: []AgentDraftProblem{{Variant: "b", Key: "sticky", Sentence: "off, user or global."}}}, messages[0])
	require.Equal(t, AgentDraftMessage{InputID: messages[2].InputID, Text: "MSG-2", Labels: map[string]int64{"a": revA, "b": dt.revision}}, messages[2])

	dt.edit(t, &AgentDraftOp{Kind: AgentDraftRemoveVariant, Variant: "b"})
	_, _, err = ct.send(t, false, "MSG-3")
	require.NoError(t, err)
	ct.collect(t, "COL-A/MSG-3")

	dt.edit(t, &AgentDraftOp{Kind: AgentDraftAddVariant, Variant: "b", Value: "a"})
	dt.edit(t, &AgentDraftOp{Kind: AgentDraftEditFile, Variant: "b", Value: "---\nmodel: gpt-6.1-sol\n---\nCOL-B\n"})
	_, _, err = ct.send(t, false, "MSG-4")
	require.NoError(t, err)
	// Variant b, removed for MSG-3 and added back, answers MSG-4 and never MSG-3.
	ct.collect(t, "COL-A/MSG-4", "COL-B/MSG-4")

	dt.edit(t, &AgentDraftOp{Kind: AgentDraftSetExternalMCP, ExternalMCP: AgentDraftExternalMCP{On: true, Caller: "zendesk-bridge"}})
	refusal = ct.refused(t, "MSG-5")
	require.True(t, refusal.Restart)
	require.EqualError(t, refusal, "This test chat began in the Web: restart it to test in External MCP mode.")

	// Restart in place is impossible: the answer running when the chat restarts stays in the old chat.
	dt.edit(t, &AgentDraftOp{Kind: AgentDraftSetExternalMCP})

	gate = ct.hold("MSG-HOLD6")
	_, _, err = ct.send(t, false, "MSG-HOLD6")
	require.NoError(t, err)
	ct.waitHeld(t, "MSG-HOLD6")

	fresh, _, err := ct.send(t, true)
	require.NoError(t, err)
	require.NotEqual(t, chat, fresh)
	// Checked while held: once delivered, the turn's row gives way to the old chat's entries.
	require.Equal(t, 1, ct.count(t, `SELECT count(*) FROM turns WHERE conversation_id = $1 AND inbound_json->>'Text' = 'MSG-HOLD6'`, chat))
	close(gate)
	ct.collect(t, "COL-A/MSG-HOLD6", "COL-B/MSG-HOLD6")
	require.Eventually(t, func() bool {
		active, err := dt.rt.Sessions.HasActiveTurn(t.Context(), chat)
		return err == nil && !active
	}, time.Minute, 10*time.Millisecond)
	require.Zero(t, ct.count(t, `SELECT count(*) FROM turns WHERE conversation_id = $1`, fresh))
	require.Zero(t, ct.count(t, `SELECT count(*) FROM entries WHERE conversation_id = $1`, fresh))

	loaded, err := dt.rt.Sessions.LoadAgentDraft(t.Context(), dt.draft.ID)
	require.NoError(t, err)
	require.Equal(t, fresh, loaded.TestChat)

	// A replay sends the old chat's messages again, in order, a teammate's too, and then the texts sent with it.
	_, _, err = ct.send(t, false, "MSG-R1")
	require.NoError(t, err)
	_, _, err = dt.rt.Sessions.SendAgentDraft(t.Context(), dt.rt, &AgentDraftSend{ID: dt.draft.ID, Username: "bob@example.com", Principal: "Bob", Texts: []string{"MSG-R2"}})
	require.NoError(t, err)
	ct.collect(t, "COL-A/MSG-R1", "COL-B/MSG-R1", "COL-A/MSG-R2", "COL-B/MSG-R2")

	// A refused replay leaves the draft on its chat, so a later one still has those messages.
	dt.edit(t, &AgentDraftOp{Kind: AgentDraftSetField, Variant: "a", Key: "sticky", Value: "maybe"})
	_, _, err = dt.rt.Sessions.SendAgentDraft(t.Context(), dt.rt, &AgentDraftSend{ID: dt.draft.ID, Username: "alice@example.com", Principal: "Alice", Restart: true, Replay: true, Texts: []string{"MSG-R3"}})
	_, ok := errors.AsType[*AgentDraftRefusedError](err)
	require.True(t, ok, "%v", err)
	loaded, err = dt.rt.Sessions.LoadAgentDraft(t.Context(), dt.draft.ID)
	require.NoError(t, err)
	require.Equal(t, fresh, loaded.TestChat)
	dt.edit(t, &AgentDraftOp{Kind: AgentDraftSetField, Variant: "a", Key: "sticky"})

	replayed, _, err := dt.rt.Sessions.SendAgentDraft(t.Context(), dt.rt, &AgentDraftSend{ID: dt.draft.ID, Username: "alice@example.com", Principal: "Alice", Restart: true, Replay: true, Texts: []string{"MSG-R3"}})
	require.NoError(t, err)
	require.NotEqual(t, fresh, replayed)

	messages = ct.messages(t, replayed)
	texts := make([]string, 0, len(messages))

	for _, message := range messages {
		texts = append(texts, message.Text)
	}

	require.Equal(t, []string{"MSG-R1", "MSG-R2", "MSG-R3"}, texts)
	ct.collect(t, "COL-A/MSG-R1", "COL-B/MSG-R1", "COL-A/MSG-R2", "COL-B/MSG-R2", "COL-A/MSG-R3", "COL-B/MSG-R3")
	require.Equal(t, []string{"COL-A/MSG-R1", "COL-A/MSG-R2", "COL-A/MSG-R3"}, slices.DeleteFunc(ct.order[len(ct.order)-6:], func(answer string) bool { return !strings.HasPrefix(answer, "COL-A/") }))

	for listed, err := range dt.rt.Sessions.SidebarChats(t.Context(), "", noSlackLookups{}, nil) {
		require.NoError(t, err)
		require.NotContains(t, []string{chat, fresh, replayed}, listed.Session.Conversation.ID)
	}
}

// In External MCP mode every column answers as a real External MCP call with the draft's pairs and caller,
// even while an earlier turn runs. A key the first message sent cannot change or go away without a restart,
// a key added later goes with its message only, and the mode cannot change mid-chat. A sticky draft is
// refused at send, and a sticky live agent does not answer.
func TestAgentDraftChatExternalMCP(t *testing.T) {
	t.Parallel()

	ct := startDraftChatTest(t, "support-bot", map[string]string{"support-bot": "---\nmodel: gpt-6.1-sol\nsticky: user\n---\nCOL-L\n"})
	dt := ct.dt
	mcp := func(on bool, pairs map[string]string) {
		dt.edit(t, &AgentDraftOp{Kind: AgentDraftSetExternalMCP, ExternalMCP: AgentDraftExternalMCP{On: on, Caller: "zendesk-bridge", Metadata: pairs}})
	}

	mcp(true, map[string]string{"customer_id": "acme-042"})

	gate := ct.hold("MSG-HOLD1")
	chat, problems, err := ct.send(t, false, "MSG-HOLD1")
	require.NoError(t, err)
	require.Equal(t, []AgentDraftProblem{{Key: "sticky", Sentence: "External MCP only serves sticky: off agents, and the live agent is not."}}, problems)
	ct.waitHeld(t, "MSG-HOLD1")
	ct.stored(t, "MSG-2")

	// The second message, queued behind the first, is still an External MCP call from the caller.
	var data []byte
	require.NoError(t, dt.rt.Sessions.db.QueryRowContext(t.Context(), `SELECT inbound_json FROM turns WHERE conversation_id = $1 AND inbound_json->>'Text' = 'MSG-2' AND phase = 'waiting'`, chat).Scan(&data))

	var second protocol.InboundMessage
	require.NoError(t, json.Unmarshal(data, &second))
	require.Equal(t, protocol.SourceExternalMCP, second.Source)
	require.Equal(t, protocol.InboundKindPrompt, second.Kind)
	require.Equal(t, map[string]string{"customer_id": "acme-042", "external_conversation_id": chat}, second.ExternalMCPMetadata)
	require.Equal(t, "zendesk-bridge", second.Metadata[protocol.InboundPrincipalMetadataKey])
	require.Empty(t, second.Metadata[protocol.InboundPrincipalEmailMetadataKey])

	close(gate)

	for answer, request := range ct.collect(t, "COL-A/MSG-HOLD1", "COL-B/MSG-HOLD1", "COL-A/MSG-2", "COL-B/MSG-2") {
		for _, want := range []string{"This external MCP thread has metadata:", `ROCKETCLAW_METADATA_CUSTOMER_ID="acme-042"`, `ROCKETCLAW_METADATA_EXTERNAL_CONVERSATION_ID="` + chat + `"`, `[ExternalMCP principal="zendesk-bridge"`} {
			require.Contains(t, request, want, answer)
		}

		require.NotContains(t, request, askUserQuestionToolName+"(", answer)
	}

	// The check reads the pairs the send captures, so a teammate's change just before it counts.
	// Keys are compared as the variables they become, so customer-id is customer_id.
	for _, pairs := range []map[string]string{{"customer_id": "acme-043"}, {"ticket": "77"}} {
		mcp(true, pairs)

		refusal := ct.refused(t, "MSG-X")
		require.True(t, refusal.Restart)
		require.EqualError(t, refusal, `External MCP keeps customer_id at "acme-042", the value this test chat's first message sent: restart the chat with the new metadata.`)
	}

	// Two keys of one variable, locked or not, would give the agent one value while the message shows both, and
	// a key posing as the conversation id would show a value the agent never gets.
	for _, refused := range []struct {
		pairs map[string]string
		want  string
	}{
		{map[string]string{"customer_id": "acme-042", "customer-id": "acme-043"}, "Metadata keys customer-id and customer_id both become ROCKETCLAW_METADATA_CUSTOMER_ID: keep one of them."},
		{map[string]string{"customer_id": "acme-042", "a-b": "1", "a_b": "1"}, "Metadata keys a-b and a_b both become ROCKETCLAW_METADATA_A_B: keep one of them."},
		{map[string]string{"customer_id": "acme-042", "external-conversation-id": "posed"}, "External MCP sets ROCKETCLAW_METADATA_EXTERNAL_CONVERSATION_ID to the conversation's id: remove the metadata key external-conversation-id."},
	} {
		mcp(true, refused.pairs)

		refusal := ct.refused(t, "MSG-X")
		require.False(t, refusal.Restart)
		require.EqualError(t, refusal, refused.want)
	}

	mcp(true, map[string]string{"customer-id": "acme-042", "ticket": "77"})
	dt.save(t, "support-bot", "---\nmodel: gpt-6.1-sol\nsticky: user\n---\nCOL-L2\n")

	_, _, err = ct.send(t, false, "MSG-3")
	require.NoError(t, err)

	for answer, request := range ct.collect(t, "COL-A/MSG-3", "COL-B/MSG-3") {
		require.Contains(t, request, "This external MCP turn has additional metadata:", answer)
		require.Contains(t, request, `ROCKETCLAW_METADATA_TICKET="77"`, answer)
	}

	mcp(true, map[string]string{"customer_id": "acme-042"})

	_, _, err = ct.send(t, false, "MSG-4")
	require.NoError(t, err)

	for answer, request := range ct.collect(t, "COL-A/MSG-4", "COL-B/MSG-4") {
		require.NotContains(t, request, "ROCKETCLAW_METADATA_TICKET", answer)
	}

	// The first message's record holds the chat's mode, keys and caller; a later key shows on its message
	// only. The live version moves only with a save, and the sticky live agent answers none.
	messages := ct.messages(t, chat)
	require.Len(t, messages, 4)

	first := AgentDraftExternalMCP{On: true, Caller: "zendesk-bridge", Metadata: map[string]string{"customer_id": "acme-042"}}
	require.Equal(t, first, messages[0].ExternalMCP)
	require.Equal(t, AgentDraftExternalMCP{On: true, Caller: "zendesk-bridge", Metadata: map[string]string{"customer-id": "acme-042", "ticket": "77"}}, messages[2].ExternalMCP)
	require.Equal(t, first, messages[3].ExternalMCP)
	require.NotEmpty(t, messages[0].Live)
	require.Equal(t, messages[0].Live, messages[1].Live)
	require.NotEqual(t, messages[1].Live, messages[2].Live)
	require.Equal(t, messages[2].Live, messages[3].Live)

	for _, message := range messages {
		require.Equal(t, []AgentDraftProblem{{Key: "sticky", Sentence: "External MCP only serves sticky: off agents, and the live agent is not."}}, message.NotRun)
	}

	mcp(false, map[string]string{"customer_id": "acme-042"})

	refusal := ct.refused(t, "MSG-5")
	require.True(t, refusal.Restart)
	require.EqualError(t, refusal, "This test chat began in External MCP mode: restart it to test in the Web.")

	// A teammate makes a variant sticky after the switch was turned on.
	mcp(true, map[string]string{"customer_id": "acme-042"})
	dt.edit(t, &AgentDraftOp{Kind: AgentDraftSetField, Variant: "b", Key: "sticky", Value: "user"})
	refusal = ct.refused(t, "MSG-6")
	require.False(t, refusal.Restart)
	require.EqualError(t, refusal, "External MCP only serves sticky: off agents: set sticky to off in every variant, or turn External MCP mode off.")

	dt.edit(t, &AgentDraftOp{Kind: AgentDraftSetField, Variant: "b", Key: "sticky"})
	// A pair cannot pose as the conversation id, even in a new chat's first message; the id is the chat's.
	mcp(true, map[string]string{"customer_id": "acme-043", "external_conversation_id": "posed"})

	_, _, err = ct.send(t, true, "MSG-R")
	require.EqualError(t, err, "External MCP sets ROCKETCLAW_METADATA_EXTERNAL_CONVERSATION_ID to the conversation's id: remove the metadata key external_conversation_id.")
	mcp(true, map[string]string{"customer_id": "acme-043"})

	fresh, _, err := ct.send(t, true, "MSG-R")
	require.NoError(t, err)
	require.NotEqual(t, chat, fresh)

	for answer, request := range ct.collect(t, "COL-A/MSG-R", "COL-B/MSG-R") {
		require.Contains(t, request, `ROCKETCLAW_METADATA_CUSTOMER_ID="acme-043"`, answer)
		require.Contains(t, request, `ROCKETCLAW_METADATA_EXTERNAL_CONVERSATION_ID="`+fresh+`"`, answer)
	}
}
