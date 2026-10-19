package backend

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/require"
)

func TestShadowConsumedUploadOwnership(t *testing.T) {
	store, _ := newShadowTestStore(t)
	ctx := t.Context()
	seed := protocol.OutboundAttachment{ID: "seed", Name: "seed.txt", MIMEType: "text/plain", Data: []byte("seed")}
	require.NoError(t, store.SaveAttachment(ctx, "parent", "", &seed, true))
	_, err := store.AppendEntryID(ctx, "parent", testSessionEntry("attachment:seed", "seed answer"))
	require.NoError(t, err)
	require.NoError(t, (&Runtime{Sessions: store}).CreateConversation(ctx, protocol.Conversation{ID: "parent", Agent: "main"}))

	var child, copiedID string
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT conversation_id FROM conversations WHERE shadow_parent IS NOT NULL ORDER BY shadow_agent LIMIT 1`).Scan(&child))
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT id FROM attachments WHERE conversation_id = $1`, child).Scan(&copiedID))
	_, copied, err := store.InputAttachments(ctx, child, "attachment:"+copiedID)
	require.NoError(t, err)
	require.Len(t, copied, 1, "seed references retain copied child ownership")
	require.Equal(t, child, copied[0].ConversationID)
	_, err = store.LoadAttachment(ctx, child, copiedID, true)
	require.NoError(t, err)

	texts := []string{"  exact initial\n", "uploaded steer"}

	for i, name := range []string{"image.png", "steer.txt"} {
		file := protocol.OutboundAttachment{ID: fmt.Sprintf("consumed-%d", i), Name: name, MIMEType: []string{"image/png", "text/plain"}[i], Data: []byte(name)}
		require.NoError(t, store.SaveAttachment(ctx, "parent", "", &file, true))
		texts[i] += fmt.Sprintf("\n\nattachment:%s %q (workspace path %q)", file.ID, name, "artifacts/uploads/"+file.ID+"/"+name)
	}

	entry := testSessionEntry(texts[0], "attachment:unconsumed")
	entry.TurnID = "consumed-child-turn"
	entry.ReplayInput[0] = json.RawMessage(fmt.Sprintf(`{"type":"message","role":"user","prompt_header":"[Web]","content":[{"type":"input_text","text":%q},{"type":"input_image","image_url":"data:image/png;base64,aW1hZ2U="}]}`, "[Web]\n\n"+texts[0]))
	entry.ReplayInput = append(entry.ReplayInput, json.RawMessage(fmt.Sprintf(`{"type":"message","role":"user","content":%q}`, texts[1])))
	combinedText := "attachment:consumed-0 attachment:consumed-1"
	entry.ReplayInput = append(entry.ReplayInput, json.RawMessage(fmt.Sprintf(`{"type":"message","role":"user","content":%q}`, combinedText)))
	entryID, err := store.AppendEntryID(ctx, child, entry)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `INSERT INTO shadow_turns (child_conversation_id, parent_source_conversation_id, parent_turn_id, child_turn_id, inbound_json, input_closed, terminal) VALUES ($1, 'parent', 'consumed-parent-turn', $2, '{}', TRUE, 'complete')`, child, entry.TurnID)
	require.NoError(t, err)

	for i, text := range texts {
		shown, files, err := store.InputAttachments(ctx, child, text)
		require.NoError(t, err)
		require.Len(t, files, 1, "post-seed initial and steer uploads remain cards after manifest payload release")
		require.Equal(t, []string{"  exact initial\n", "uploaded steer"}[i], shown)
		require.Equal(t, fmt.Sprintf("consumed-%d", i), files[0].ID)
		require.Equal(t, "parent", files[0].ConversationID)

		for _, uploadOnly := range []bool{false, true} {
			_, err = store.LoadAttachment(ctx, child, files[0].ID, uploadOnly)
			require.ErrorIs(t, err, sql.ErrNoRows, "ordinary load APIs never inherit parent upload access")
		}
	}

	_, combined, err := store.InputAttachments(ctx, child, combinedText)
	require.NoError(t, err)
	require.Len(t, combined, 2)
	require.Equal(t, "consumed-0", combined[0].ID)
	require.Equal(t, "consumed-1", combined[1].ID)

	for _, column := range []string{"parent_source_conversation_id", "child_turn_id"} {
		_, err = store.db.ExecContext(ctx, "UPDATE shadow_turns SET "+column+" = 'unrelated' WHERE child_conversation_id = $1", child)
		require.NoError(t, err)
		_, files, err := store.InputAttachments(ctx, child, texts[0])
		require.NoError(t, err)
		require.Empty(t, files, "surviving input requires its exact source/child-turn correlation")

		_, err = store.db.ExecContext(ctx, `UPDATE shadow_turns SET parent_source_conversation_id = 'parent', child_turn_id = $2 WHERE child_conversation_id = $1`, child, entry.TurnID)
		require.NoError(t, err)
	}

	unconsumed := protocol.OutboundAttachment{ID: "unconsumed", Name: "private.txt", Data: []byte("private")}
	require.NoError(t, store.SaveAttachment(ctx, "parent", "", &unconsumed, true))
	_, files, err := store.InputAttachments(ctx, child, "attachment:unconsumed")
	require.NoError(t, err)
	require.Empty(t, files, "an assistant's reference does not prove parent input consumption")

	_, err = store.db.ExecContext(ctx, `UPDATE conversations SET revert_message_id = $2 WHERE conversation_id = $1`, child, fmt.Sprintf("%d:1", entryID))
	require.NoError(t, err)
	_, files, err = store.InputAttachments(ctx, child, texts[1])
	require.NoError(t, err)
	require.Empty(t, files, "hidden consumed steer is not an attachment grant")

	_, err = store.db.ExecContext(ctx, `DELETE FROM entries WHERE conversation_id = $1`, child)
	require.NoError(t, err)
	_, files, err = store.InputAttachments(ctx, child, texts[0])
	require.NoError(t, err)
	require.Empty(t, files, "durable correlation alone cannot grant a removed reference")
}
