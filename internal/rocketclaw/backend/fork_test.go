package backend

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/require"
)

func TestForkConversation(t *testing.T) {
	sessions, err := NewSessionService(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	runtime := &Runtime{Sessions: sessions}
	require.NoError(t, runtime.CreateConversation(t.Context(), protocol.Conversation{ID: "source", Agent: "main"}))
	attachment := protocol.OutboundAttachment{ID: "original-file", Name: "notes.txt", MIMEType: "text/plain", Data: []byte("notes")}
	require.NoError(t, sessions.SaveAttachment(t.Context(), "source", &attachment, true))

	entry := rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now(), ResponseID: "response", ReplayInput: []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","content":"first"}`),
		json.RawMessage(`{"type":"message","role":"assistant","content":"answer"}`),
		json.RawMessage(`{"type":"message","role":"user","content":"second attachment:original-file"}`),
		json.RawMessage(`{"type":"message","role":"assistant","content":"future"}`),
	}}
	id, err := sessions.AppendEntryID(t.Context(), "source", &entry)
	require.NoError(t, err)
	// OpenCode V2: packages/app/src/session/commands/fork-dialog.tsx passes
	// before: message.id and restores that user message to the composer.
	for _, tt := range []struct {
		name, before, prompt string
		items                int
	}{
		{"first", fmt.Sprintf("%d:0", id), "first", 0},
		{"middle", fmt.Sprintf("%d:2", id), "second attachment:", 2},
		{"full", "", "", 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			prompt, err := sessions.ForkConversation(t.Context(), "source", protocol.Conversation{ID: tt.name, Agent: "main", CreatedBy: "alice"}, tt.before)
			require.NoError(t, err)
			require.Contains(t, prompt, tt.prompt)

			var parent string
			require.NoError(t, sessions.db.QueryRowContext(t.Context(), `SELECT forked_from FROM managed_conversations WHERE conversation_id = $1`, tt.name).Scan(&parent))
			require.Equal(t, "source", parent)
			require.NotContains(t, prompt, "original-file")
			entries, err := sessions.ObserveEntries(t.Context(), tt.name)
			require.NoError(t, err)

			var items []json.RawMessage
			for _, copied := range entries {
				items = append(items, copied.Entry.ReplayInput...)
			}

			require.Len(t, items, tt.items)

			if tt.name == "middle" {
				require.Empty(t, entries[0].Entry.ResponseID)
				file, err := sessions.LoadAttachment(t.Context(), tt.name, prompt[len(tt.prompt):], true)
				require.NoError(t, err)
				require.Equal(t, attachment.Data, file.Data)
			}
		})
	}

	_, err = sessions.ForkConversation(t.Context(), "source", protocol.Conversation{ID: "invalid", Agent: "main"}, fmt.Sprintf("%d:1", id))
	require.Error(t, err)
	_, err = sessions.ForkConversation(t.Context(), "source", protocol.Conversation{ID: "invalid", Agent: "main"}, "missing:0")
	require.ErrorContains(t, err, "fork message is no longer in the session")
	_, recorded, err := sessions.Thread("invalid")
	require.NoError(t, err)
	require.False(t, recorded)
	_, err = sessions.ForkConversation(t.Context(), "source", protocol.Conversation{ID: "source", Agent: "main"}, "")
	require.ErrorContains(t, err, "create fork")
	_, err = sessions.ForkConversation(t.Context(), "middle", protocol.Conversation{ID: "child", Agent: "main"}, "")
	require.NoError(t, err)
	_, err = sessions.DeleteSession(t.Context(), "source")
	require.NoError(t, err)
	entries, err := sessions.ObserveEntries(t.Context(), "middle")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Contains(t, string(entries[0].Entry.ReplayInput[1]), "answer")

	parents := map[string]string{"first": "source", "middle": "source", "full": "source", "child": "middle"}

	for row, err := range sessions.SidebarSessions(t.Context(), time.Time{}) {
		require.NoError(t, err)
		require.Equal(t, parents[row.Conversation.ID], row.ForkedFrom)
		delete(parents, row.Conversation.ID)
	}

	require.Empty(t, parents)
}

func TestForkConversationRollsBackFailedCopies(t *testing.T) {
	for _, test := range []struct {
		name, constraint, message string
	}{
		{"attachment", "ALTER TABLE attachments ADD CONSTRAINT reject_fork CHECK (conversation_id <> 'fork')", "record fork attachment"},
		{"summary", "ALTER TABLE session_summaries ADD CONSTRAINT reject_fork CHECK (conversation_id <> 'fork')", "write session summary"},
		{"entry", "ALTER TABLE session_entries ADD CONSTRAINT reject_fork CHECK (conversation_id <> 'fork')", "append rocketcode session entry"},
		{"commit", "ALTER TABLE managed_conversations ADD CONSTRAINT reject_fork UNIQUE (agent) DEFERRABLE INITIALLY DEFERRED", "commit fork"},
		{"missing file", "", "read fork attachment"},
		{"read only files", "", "copy fork attachment"},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			sessions, err := NewSessionService(workspace)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

			runtime := &Runtime{Sessions: sessions}
			require.NoError(t, runtime.CreateConversation(t.Context(), protocol.Conversation{ID: "source", Agent: "main"}))
			attachment := protocol.OutboundAttachment{ID: "original", Name: "notes.txt", MIMEType: "text/plain", Data: []byte("notes")}
			require.NoError(t, sessions.SaveAttachment(t.Context(), "source", &attachment, true))

			entry := rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now(), ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"attachment:original"}`)}}
			_, err = sessions.AppendEntryID(t.Context(), "source", &entry)
			require.NoError(t, err)

			if test.constraint != "" {
				_, err = sessions.db.ExecContext(t.Context(), test.constraint)
				require.NoError(t, err)
			} else {
				storage := sessions.attachments.(filesystemAttachments)
				root, err := os.OpenRoot(storage.path)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, root.Close()) })

				if test.name == "missing file" {
					require.NoError(t, root.Remove(attachment.ID))
				} else {
					require.NoError(t, root.Chmod(".", 0o500))
					t.Cleanup(func() { require.NoError(t, root.Chmod(".", 0o700)) })
				}
			}

			_, err = sessions.ForkConversation(t.Context(), "source", protocol.Conversation{ID: "fork", Agent: "main"}, "")
			require.ErrorContains(t, err, test.message)
			_, recorded, err := sessions.Thread("fork")
			require.NoError(t, err)
			require.False(t, recorded)
			entries, err := sessions.ObserveEntries(t.Context(), "fork")
			require.NoError(t, err)
			require.Empty(t, entries)

			for _, table := range []string{"attachments", "session_summaries"} {
				var count int
				require.NoError(t, sessions.db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table+" WHERE conversation_id = 'fork'").Scan(&count))
				require.Zero(t, count, table)
			}

			entries, err = sessions.ObserveEntries(t.Context(), "source")
			require.NoError(t, err)
			require.Len(t, entries, 1)
			require.Equal(t, entry.ReplayInput, entries[0].Entry.ReplayInput)
		})
	}
}

func TestForkConversationRejectsUnreadableHistory(t *testing.T) {
	sessions, err := NewSessionService(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	for _, raw := range []string{
		`{"replay_input":[{"type":"compaction","content":42}]}`,
		`{"replay_input":42}`,
	} {
		var id int64
		require.NoError(t, sessions.db.QueryRowContext(t.Context(), `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('source', $1, '') RETURNING id`, raw).Scan(&id))
		_, err := sessions.ForkConversation(t.Context(), "source", protocol.Conversation{ID: "fork", Agent: "main"}, fmt.Sprintf("%d:0", id))
		require.Error(t, err, raw)
		_, recorded, err := sessions.Thread("fork")
		require.NoError(t, err)
		require.False(t, recorded)
		_, err = sessions.db.ExecContext(t.Context(), `DELETE FROM session_entries WHERE id = $1`, id)
		require.NoError(t, err)
	}

	_, err = sessions.db.ExecContext(t.Context(), `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('source', '{"sync_source_entry_id":-1,"replay_input":[{"type":"message","role":"user","content":"deleted producer"}]}', '')`)
	require.NoError(t, err)
	_, err = sessions.ForkConversation(t.Context(), "source", protocol.Conversation{ID: "fork", Agent: "main"}, "")
	require.NoError(t, err)
	entries, err := sessions.ObserveEntries(t.Context(), "fork")
	require.NoError(t, err)
	require.Empty(t, entries, "history from a deleted producer must not be resurrected")
}
