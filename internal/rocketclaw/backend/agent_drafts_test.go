package backend

import (
	"cmp"
	"encoding/json"
	"errors"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
	"github.com/stretchr/testify/require"
)

// The shared vectors pin the body transform the Web client also runs, including the co-editing model's
// counterexamples: a delete widened over an insert, a clipped delete, and a dropped insert all fail them.
func TestAgentDraftBodyTransformVectors(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join("testdata", "body_transform.json"))
	require.NoError(t, err)

	var vectors struct {
		Cases []struct {
			Name, Text, Result   string
			A, B, APastB, BPastA []AgentDraftSplice
			AFirst               bool
		}
	}
	require.NoError(t, json.Unmarshal(data, &vectors))
	require.NotEmpty(t, vectors.Cases)

	apply := func(text string, splices []AgentDraftSplice) string {
		draft := &AgentDraft{Variants: []AgentDraftVariant{{Name: "a", Body: text}}}
		require.NoError(t, applyAgentDraftOp(draft, &AgentDraftOp{Kind: AgentDraftEditBody, Variant: "a", Splices: splices}))

		return draft.Variants[0].Body
	}

	for _, c := range vectors.Cases {
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()

			aPast, bPast := transformAgentDraftSplices(c.A, c.B, c.AFirst)
			// Appending to an empty slice lets an expected [] equal no splices.
			require.Equal(t, c.APastB, append([]AgentDraftSplice{}, aPast...))
			require.Equal(t, c.BPastA, append([]AgentDraftSplice{}, bPast...))
			require.Equal(t, c.Result, apply(apply(c.Text, c.B), aPast))
			require.Equal(t, c.Result, apply(apply(c.Text, c.A), bPast))
		})
	}
}

// A File edit is diffed against the body at its base revision, so a teammate's later edit survives it and
// text the typist never touched is never typed again.
func TestAgentDraftFileEditKeepsTeammateEdits(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()

	for _, tt := range []struct {
		name     string
		teammate []AgentDraftSplice
		want     string
	}{
		{"teammate types before X", []AgentDraftSplice{{At: 0, Insert: "b"}}, "baX"},
		{"teammate deletes X", []AgentDraftSplice{{At: 0, Remove: 1}}, "a"},
	} {
		draft, err := store.CreateAgentDraft(ctx, "alice", AgentDraftOrigin{Name: "bot", Mode: harness.AgentModeAdvanced})
		require.NoError(t, err)
		base := applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: 1, OpID: "alice.1", Author: "alice", Kind: AgentDraftEditBody, Variant: "a", Splices: []AgentDraftSplice{{At: 0, Insert: "XY"}}})
		applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: base, OpID: "bob.1", Author: "bob", Kind: AgentDraftEditBody, Variant: "a", Splices: tt.teammate})

		// Two separate changes from the file at base: "XY" becomes "aX".
		file := string(renderAgentDraftFile(harness.AgentModeAdvanced, &AgentDraftVariant{Body: "aX"}))
		revision := applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: base, OpID: "alice.2", Author: "alice", Kind: AgentDraftEditFile, Variant: "a", Value: file})

		got, err := store.LoadAgentDraft(ctx, draft.ID)
		require.NoError(t, err, tt.name)
		require.Equal(t, tt.want, got.Variants[0].Body, tt.name)

		ops, err := store.AgentDraftOps(ctx, draft.ID, revision-1)
		require.NoError(t, err)
		require.Len(t, ops, 1, tt.name)
		require.Equal(t, AgentDraftEditBody, ops[0].Kind)
		require.Len(t, ops[0].Splices, 2, tt.name)
	}
}

func TestAgentDraftFileEdits(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	draft, err := store.CreateAgentDraft(ctx, "alice", AgentDraftOrigin{Name: "bot", Mode: harness.AgentModeAdvanced})
	require.NoError(t, err)

	base := applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: 1, OpID: "alice.1", Author: "alice", Kind: AgentDraftEditFile, Variant: "a",
		Value: "---\nspace: Main\nmodel: gpt-5\n---\nline one\nline two\n"})

	// Text that does not parse yet stores nothing and names the line, counted in the whole file.
	for file, line := range map[string]int{
		"---\nspace: Main\nmodel: gpt-5: x\n---\nline one\n": 3,
		"no frontmatter\n":                       1,
		"---\n- a list\n---\n":                   2,
		"---\nmodel: gpt-5\nmode: simple\n---\n": 3,
		"---\nmodel: gpt-5\nmodel: gpt-6\n---\n": 3,
	} {
		_, err = store.ApplyAgentDraftOp(ctx, draft.ID, &AgentDraftOp{BaseRevision: base, OpID: "alice.2", Author: "alice", Kind: AgentDraftEditFile, Variant: "a", Value: file})
		notValid, ok := errors.AsType[*AgentDraftNotValidError](err)
		require.True(t, ok, "%q: %v", file, err)
		require.Equal(t, line, notValid.Line, file)
	}

	require.EqualError(t, &AgentDraftNotValidError{Line: 3}, "line 3 isn't valid yet")

	_, err = store.ApplyAgentDraftOp(ctx, draft.ID, &AgentDraftOp{BaseRevision: base, Kind: AgentDraftEditFile, Variant: "z", Value: "---\nmodel: x\n---\n"})
	require.EqualError(t, err, "That variant does not exist.")

	unchanged, err := store.LoadAgentDraft(ctx, draft.ID)
	require.NoError(t, err)
	require.Equal(t, base, unchanged.Revision)

	// A teammate's later space survives a file edit of model and one instructions line made at base.
	teammate := applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: base, OpID: "bob.1", Author: "bob", Kind: AgentDraftSetField, Variant: "a", Key: "space", Value: "Ops"})
	applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: base, OpID: "alice.3", Author: "alice", Kind: AgentDraftEditFile, Variant: "a",
		Value: "---\nspace: Main\nmodel: gpt-6\n---\nline one\nline 2\n"})

	ops, err := store.AgentDraftOps(ctx, draft.ID, teammate)
	require.NoError(t, err)
	require.Len(t, ops, 2)
	require.Equal(t, []AgentDraftOpKind{AgentDraftSetField, AgentDraftEditBody}, []AgentDraftOpKind{ops[0].Kind, ops[1].Kind})
	require.Equal(t, "model", ops[0].Key)

	got, err := store.LoadAgentDraft(ctx, draft.ID)
	require.NoError(t, err)
	require.Equal(t, []AgentDraftSetting{{Key: "space", Value: "Ops"}, {Key: "model", Value: "gpt-6"}}, got.Variants[0].Settings)
	require.Equal(t, "line one\nline 2\n", got.Variants[0].Body)

	// A setting left out of the file is removed.
	applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: got.Revision, Kind: AgentDraftEditFile, Variant: "a", Value: "---\nspace: Ops\n---\nline one\nline 2\n"})
	got, err = store.LoadAgentDraft(ctx, draft.ID)
	require.NoError(t, err)
	require.Equal(t, []AgentDraftSetting{{Key: "space", Value: "Ops"}}, got.Variants[0].Settings)
}

// The file diff turns one text into the other with the fewest code points removed and inserted.
func TestAgentDraftBodyDiff(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 1500)
	for _, tt := range []struct {
		from, to string
		edits    int
	}{
		{"XY", "aX", 2},
		{"abcabba", "cbabac", 5},
		{"aaa", "aXaa", 1},
		{"same", "same", 0},
		{"héllo wörld", "hello world!", 5},
		// Past 2,000 edits the changed middle is replaced whole.
		{long, strings.Repeat("b", 1500), 3000},
	} {
		splices := diffAgentDraftBody(tt.from, tt.to)
		draft := &AgentDraft{Variants: []AgentDraftVariant{{Name: "a", Body: tt.from}}}
		require.NoError(t, applyAgentDraftOp(draft, &AgentDraftOp{Kind: AgentDraftEditBody, Variant: "a", Splices: splices}))
		require.Equal(t, tt.to, draft.Variants[0].Body)

		edits := 0
		for _, splice := range splices {
			edits += splice.Remove + utf8.RuneCountInString(splice.Insert)
		}

		require.Equal(t, tt.edits, edits, tt.from)
	}

	require.Equal(t, []AgentDraftSplice{{At: 0, Insert: "a"}, {At: 2, Remove: 1}}, diffAgentDraftBody("XY", "aX"))
	require.Equal(t, []AgentDraftSplice{{At: 0, Remove: 1500, Insert: strings.Repeat("b", 1500)}}, diffAgentDraftBody(long, strings.Repeat("b", 1500)))
}

// Settings take the last write, and History keeps every write with its author.
func TestAgentDraftSettingsLastWriteWins(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	draft, err := store.CreateAgentDraft(ctx, "alice", AgentDraftOrigin{Name: "bot", Mode: harness.AgentModeAdvanced})
	require.NoError(t, err)

	changes, stop := iter.Pull2(store.Changes(ctx, "draft:"+draft.ID))
	t.Cleanup(stop)

	_, _, ok := changes()
	require.True(t, ok)

	applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: 1, OpID: "alice.1", Author: "alice", Kind: AgentDraftSetField, Variant: "a", Key: "model", Value: "gpt-5"})
	applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: 1, OpID: "bob.1", Author: "bob", Kind: AgentDraftSetField, Variant: "a", Key: "model", Value: "gpt-6"})

	change, err, ok := changes()
	require.True(t, ok)
	require.NoError(t, err)
	require.Equal(t, "draft:"+draft.ID, change.ConversationID)

	got, err := store.LoadAgentDraft(ctx, draft.ID)
	require.NoError(t, err)
	require.Equal(t, []AgentDraftSetting{{Key: "model", Value: "gpt-6"}}, got.Variants[0].Settings)

	ops, err := store.AgentDraftOps(ctx, draft.ID, 1)
	require.NoError(t, err)

	type history struct {
		revision            int64
		opID, author, value string
	}

	got2 := make([]history, 0, len(ops))
	for _, op := range ops {
		got2 = append(got2, history{op.Revision, op.OpID, op.Author, op.Value})
	}

	require.Equal(t, []history{{2, "alice.1", "alice", "gpt-5"}, {3, "bob.1", "bob", "gpt-6"}}, got2)

	recordTestConversations(t, store, "bot", map[string]ConversationKind{"draft-chat": KindAgentDraft})
}

// Each op either commits as the next revision or is refused and commits nothing.
func TestAgentDraftOps(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	draft, err := store.CreateAgentDraft(ctx, "alice", AgentDraftOrigin{Name: "bot", Mode: harness.AgentModeAdvanced})
	require.NoError(t, err)

	mcp := AgentDraftExternalMCP{On: true, Caller: "zendesk", Metadata: map[string]string{"customer_id": "acme"}}

	steps := []struct {
		op      AgentDraftOp
		problem string
	}{
		{op: AgentDraftOp{Kind: AgentDraftAddVariant, Variant: "b", Value: "a"}},
		{op: AgentDraftOp{Kind: AgentDraftRenameVariant, Variant: "b", Value: "c"}},
		{op: AgentDraftOp{Kind: AgentDraftRenameVariant, Variant: "c", Value: "a"}, problem: "A variant with that name exists."},
		{op: AgentDraftOp{Kind: AgentDraftAddVariant, Variant: "d", Value: "b"}, problem: "That variant does not exist."},
		{op: AgentDraftOp{Kind: AgentDraftSetTestChat, Value: "chat-1"}},
		{op: AgentDraftOp{Kind: AgentDraftSetExternalMCP, ExternalMCP: mcp}},
		{op: AgentDraftOp{Kind: AgentDraftSetSaveState, Value: "saving"}},
		{op: AgentDraftOp{Kind: AgentDraftSetSaveState, Value: "done"}, problem: "That is not a save state."},
		{op: AgentDraftOp{Kind: AgentDraftSetMode, Value: "simple"}, problem: "An agent is easy or advanced."},
		{op: AgentDraftOp{Kind: AgentDraftSetField, Variant: "a", Key: "mode", Value: "easy"}, problem: "Change the agent type with the mode."},
		{op: AgentDraftOp{Kind: AgentDraftSetField, Variant: "a", Key: "model", Value: "[gpt"}, problem: "line 1 isn't valid yet"},
		{op: AgentDraftOp{Kind: AgentDraftEditBody, Variant: "a", Splices: []AgentDraftSplice{{At: 1, Insert: "x"}}}, problem: "The change is outside the instructions."},
		{op: AgentDraftOp{Kind: AgentDraftCreate}, problem: "That change cannot be made to a draft."},
		{op: AgentDraftOp{Kind: "delete"}, problem: "That change cannot be made to a draft."},
		{op: AgentDraftOp{Kind: AgentDraftRemoveVariant, Variant: "c"}},
		{op: AgentDraftOp{Kind: AgentDraftRemoveVariant, Variant: "a"}, problem: "A draft keeps at least one variant."},
	}
	for i := range steps {
		step := &steps[i]

		before, err := store.LoadAgentDraft(ctx, draft.ID)
		require.NoError(t, err)

		step.op.BaseRevision = before.Revision
		revision, err := store.ApplyAgentDraftOp(ctx, draft.ID, &step.op)

		if step.problem != "" {
			require.EqualError(t, err, step.problem, step.op.Kind)

			after, err := store.LoadAgentDraft(ctx, draft.ID)
			require.NoError(t, err)
			require.Equal(t, before, after, step.op.Kind)

			continue
		}

		require.NoError(t, err, step.op.Kind)
		require.Equal(t, before.Revision+1, revision, step.op.Kind)
	}

	got, err := store.LoadAgentDraft(ctx, draft.ID)
	require.NoError(t, err)
	require.Equal(t, []AgentDraftVariant{{Name: "a"}}, got.Variants)
	require.Equal(t, "chat-1", got.TestChat)
	require.Equal(t, mcp, got.ExternalMCP)
	require.Equal(t, AgentDraftSaving, got.SaveState)

	// An instructions edit made before its variant was removed commits, so its author sees it acknowledged,
	// and changes nothing.
	removed := applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: got.Revision, Kind: AgentDraftAddVariant, Variant: "b", Value: "a"})
	applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: removed, Kind: AgentDraftRemoveVariant, Variant: "b"})
	late := applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: removed, OpID: "bob.9", Kind: AgentDraftEditBody, Variant: "b", Splices: []AgentDraftSplice{{At: 0, Insert: "late"}}})

	ops, err := store.AgentDraftOps(ctx, draft.ID, late-1)
	require.NoError(t, err)
	require.Equal(t, "bob.9", ops[0].OpID)

	// A setting written to a variant renamed since lands on its new name.
	named := applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: late, Kind: AgentDraftAddVariant, Variant: "b", Value: "a"})
	applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: named, Kind: AgentDraftRenameVariant, Variant: "b", Value: "fast"})
	applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: named, Kind: AgentDraftSetField, Variant: "b", Key: "model", Value: "gpt-5-mini"})

	drafts, err := store.ListAgentDrafts(ctx)
	require.NoError(t, err)
	require.Len(t, drafts, 1)
	require.Equal(t, []AgentDraftVariant{{Name: "a"}, {Name: "fast", Settings: []AgentDraftSetting{{Key: "model", Value: "gpt-5-mini"}}}}, drafts[0].Variants)

	_, err = store.ApplyAgentDraftOp(ctx, draft.ID, &AgentDraftOp{BaseRevision: drafts[0].Revision + 1, Kind: AgentDraftSetTestChat})
	require.EqualError(t, err, "The change was made on a version this draft does not have.")
}

// Switching mode converts every variant; what the switch dropped comes back through Restore.
func TestAgentDraftModeSwitchConvertsEveryVariant(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	draft, err := store.CreateAgentDraft(ctx, "alice", AgentDraftOrigin{Name: "helper", Template: "assistant"})
	require.NoError(t, err)
	require.Equal(t, harness.AgentModeEasy, draft.Mode)

	revision := applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: 1, OpID: "alice.1", Kind: AgentDraftSetField, Variant: "a", Key: "subagents", Value: "[]"})
	revision = applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: revision, OpID: "alice.2", Kind: AgentDraftAddVariant, Variant: "b", Value: "a"})
	easy := applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: revision, OpID: "alice.3", Kind: AgentDraftAddVariant, Variant: "c", Value: "a"})

	keys := func(revisionWant int64, mode harness.AgentMode) [][]string {
		got, err := store.LoadAgentDraft(ctx, draft.ID)
		require.NoError(t, err)
		require.Equal(t, revisionWant, got.Revision)
		require.Equal(t, mode, got.Mode)

		all := make([][]string, 0, len(got.Variants))
		for _, variant := range got.Variants {
			names := make([]string, 0, len(variant.Settings))
			for _, setting := range variant.Settings {
				names = append(names, setting.Key)
			}

			all = append(all, names)
		}

		return all
	}

	easyKeys := []string{"space", "model", "workingDirectory", "subagents"}
	advancedKeys := []string{"space", "model", "workingDirectory"}

	// Editing variant b's file to advanced commits one mode change, which converts all three.
	b, err := store.LoadAgentDraft(ctx, draft.ID)
	require.NoError(t, err)

	file := string(renderAgentDraftFile(harness.AgentModeAdvanced, &b.Variants[1]))
	advanced := applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: easy, OpID: "alice.4", Kind: AgentDraftEditFile, Variant: "b", Value: file})
	require.Equal(t, easy+1, advanced)
	require.Equal(t, [][]string{advancedKeys, advancedKeys, advancedKeys}, keys(advanced, harness.AgentModeAdvanced))

	restored := applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: advanced, OpID: "alice.5", Kind: AgentDraftRestore, From: easy})
	require.Equal(t, [][]string{easyKeys, easyKeys, easyKeys}, keys(restored, harness.AgentModeEasy))
}

// A copy holds its file's settings and instructions and renders back in the standard order, with
// settings the help does not know after the known ones, in their own order.
func TestAgentDraftCopyRendersInStandardOrder(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	store := newTestSessionServiceAt(t, workspace)
	ctx := t.Context()

	for _, tt := range []struct {
		name, file, rendered string
		settings             []AgentDraftSetting
	}{
		{
			name:     "support-bot",
			file:     "---\nmode: easy\nspace: Support\nmodel: gpt-5#low\nworkingDirectory: sessions/support/$ROCKETCLAW_CONVERSATION_ID\nrocketclaw:\n  - pages\n---\n\nHelp customers.\n",
			settings: []AgentDraftSetting{{"space", "Support"}, {"model", "gpt-5#low"}, {"workingDirectory", "sessions/support/$ROCKETCLAW_CONVERSATION_ID"}, {"rocketclaw", "- pages"}},
		},
		{
			name:     "tuned",
			file:     "---\ndescription: Tuned\ntemperature: 0.2\nmodel: gpt-5\nhidden: true\npermission:\n  bash:\n    \"gh *\": \"allow\"\n---\nBe brief.\n",
			rendered: "---\ndescription: Tuned\nmodel: gpt-5\npermission:\n  bash:\n    \"gh *\": \"allow\"\ntemperature: 0.2\nhidden: true\n---\nBe brief.\n",
			settings: []AgentDraftSetting{{"description", "Tuned"}, {"temperature", "0.2"}, {"model", "gpt-5"}, {"hidden", "true"}, {"permission", "bash:\n  \"gh *\": \"allow\""}},
		},
	} {
		require.NoError(t, os.WriteFile(filepath.Join(workspace, config.DefaultRuntimeDir, "agents", tt.name+".md"), []byte(tt.file), 0o600))

		draft, err := store.CreateAgentDraft(ctx, "alice", AgentDraftOrigin{Name: tt.name, Copy: true})
		require.NoError(t, err)
		require.Equal(t, tt.settings, draft.Variants[0].Settings, tt.name)
		require.Equal(t, cmp.Or(tt.rendered, tt.file), string(renderAgentDraftFile(draft.Mode, &draft.Variants[0])), tt.name)
	}
}

// Create, copy and rename take only names that are one plain path segment.
func TestAgentDraftNameRule(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	draft, err := store.CreateAgentDraft(ctx, "alice", AgentDraftOrigin{Name: "bot.v2_x-y", Mode: harness.AgentModeEasy})
	require.NoError(t, err)

	for _, name := range []string{"../x", "a/b", "--upload-pack=x", ".hidden", ""} {
		_, errCreate := store.CreateAgentDraft(ctx, "alice", AgentDraftOrigin{Name: name, Mode: harness.AgentModeEasy})
		_, errCopy := store.CreateAgentDraft(ctx, "alice", AgentDraftOrigin{Name: name, Copy: true})
		_, errRename := store.ApplyAgentDraftOp(ctx, draft.ID, &AgentDraftOp{BaseRevision: 1, Kind: AgentDraftRename, Value: name})
		_, errVariant := store.ApplyAgentDraftOp(ctx, draft.ID, &AgentDraftOp{BaseRevision: 1, Kind: AgentDraftAddVariant, Variant: name, Value: "a"})

		for _, err := range []error{errCreate, errCopy, errRename, errVariant} {
			nameErr, ok := errors.AsType[*AgentDraftNameError](err)
			require.True(t, ok, "%q: %v", name, err)
			require.Equal(t, name, nameErr.Name)
		}
	}

	_, err = store.CreateAgentDraft(ctx, "alice", AgentDraftOrigin{Name: "blank"})
	require.EqualError(t, err, "An agent is easy or advanced.")
	require.EqualError(t, &AgentDraftNameError{Name: "a/b"}, `"a/b" can't be a name: use only letters, digits, '.', '_' and '-', and don't start with '.' or '-'.`)
}

// A draft records each save destination's file for its name when it takes the name, absence included.
func TestAgentDraftRecordsLayerBases(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	store := newTestSessionServiceAt(t, workspace)
	ctx := t.Context()
	overlay := "github.com/acme/team@main"
	store.definitions = config.NewLockedConfig(&config.Config{Workspace: workspace, Overlays: []string{overlay}})

	clone := skel.OverlayInfos(workspace, config.DefaultRuntimeDir, []string{overlay})[0].ClonePath
	require.NoError(t, os.MkdirAll(filepath.Join(clone, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(clone, "agents", "triage.md"), []byte("team triage"), 0o600))

	draft, err := store.CreateAgentDraft(ctx, "alice", AgentDraftOrigin{Name: "triage", Mode: harness.AgentModeEasy})
	require.NoError(t, err)
	require.Equal(t, map[string]*string{"workspace": nil, overlay: new("team triage")}, draft.Bases)

	require.NoError(t, os.Mkdir(filepath.Join(workspace, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "agents", "fresh.md"), []byte("ours"), 0o600))
	applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: 1, Kind: AgentDraftRename, Value: "fresh"})

	renamed, err := store.LoadAgentDraft(ctx, draft.ID)
	require.NoError(t, err)
	require.Equal(t, "fresh", renamed.Name)
	require.Equal(t, map[string]*string{"workspace": new("ours"), overlay: nil}, renamed.Bases)
}

func TestValidateAgentDraft(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()

	for _, template := range AgentDraftTemplates() {
		draft, err := store.CreateAgentDraft(ctx, "alice", AgentDraftOrigin{Name: template, Template: template})
		require.NoError(t, err)

		problems, err := store.ValidateAgentDraft(draft)
		require.NoError(t, err)
		require.Empty(t, problems, template)
	}

	draft, err := store.CreateAgentDraft(ctx, "alice", AgentDraftOrigin{Name: "helper", Template: "assistant"})
	require.NoError(t, err)
	applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: 1, Kind: AgentDraftSetField, Variant: "a", Key: "space"})
	draft, err = store.LoadAgentDraft(ctx, draft.ID)
	require.NoError(t, err)

	problems, err := store.ValidateAgentDraft(draft)
	require.NoError(t, err)
	require.Equal(t, []AgentDraftProblem{{Variant: "a", Key: "space", Sentence: "A space name, as one string."}}, problems)

	// A key easy agents lack, and an easy folder without variables inside the workspace, which the folder
	// check refuses; one outside it passes.
	revision := applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: draft.Revision, Kind: AgentDraftSetField, Variant: "a", Key: "space", Value: "Main"})
	revision = applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: revision, Kind: AgentDraftAddVariant, Variant: "b", Value: "a"})
	revision = applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: revision, Kind: AgentDraftAddVariant, Variant: "c", Value: "a"})
	revision = applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: revision, Kind: AgentDraftSetField, Variant: "a", Key: "temperature", Value: "0.2"})
	workspace := store.definitions.Clone().Workspace
	revision = applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: revision, Kind: AgentDraftSetField, Variant: "b", Key: "workingDirectory", Value: filepath.Join(workspace, "agents", "helper")})
	applyTestAgentDraftOp(t, store, draft.ID, &AgentDraftOp{BaseRevision: revision, Kind: AgentDraftSetField, Variant: "c", Key: "workingDirectory", Value: t.TempDir()})
	draft, err = store.LoadAgentDraft(ctx, draft.ID)
	require.NoError(t, err)

	problems, err = store.ValidateAgentDraft(draft)
	require.NoError(t, err)
	require.Equal(t, []AgentDraftProblem{
		{Variant: "a", Key: "temperature", Sentence: "This setting isn't valid here yet."},
		{Variant: "b", Key: "workingDirectory", Sentence: "A folder that is not, does not contain, and is not inside the workspace or RocketClaw's own folders."},
	}, problems)

	_, err = store.CreateAgentDraft(ctx, "alice", AgentDraftOrigin{Name: "x", Template: "nope"})
	require.ErrorIs(t, err, fs.ErrNotExist)
	_, err = store.CreateAgentDraft(ctx, "alice", AgentDraftOrigin{Name: "missing", Copy: true})
	require.ErrorIs(t, err, fs.ErrNotExist)
}

func applyTestAgentDraftOp(t *testing.T, store *SessionService, id string, op *AgentDraftOp) int64 {
	t.Helper()

	revision, err := store.ApplyAgentDraftOp(t.Context(), id, op)
	require.NoError(t, err)

	return revision
}
