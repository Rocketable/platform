package backend

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
	"github.com/stretchr/testify/require"
)

const supportBot = "---\nmodel: gpt-6.1-sol\n---\nHelp.\n"

// agentDraftSave runs Run over a workspace; validate runs in each of Reload's validations, a save's check of
// its stage included, with the staged runtime directory, and stop ends the run.
type agentDraftSave struct {
	rt        *Runtime
	workspace string
	validate  func(stagedWorkDir string) error
	stop      func()
}

func startAgentDraftSave(t *testing.T, workspace, dsn string, overlays ...string) *agentDraftSave {
	t.Helper()

	ds := &agentDraftSave{workspace: workspace, validate: func(string) error { return nil }}
	runtimes := make(chan *Runtime, 1)
	assembler := &frontendAssemblerMock{
		ValidateAssetsFunc: func(_ *config.Config, stagedWorkDir string) error { return ds.validate(stagedWorkDir) },
		AssembleFunc: func(rt *Runtime) (<-chan struct{}, []func(context.Context) error, error) {
			runtimes <- rt
			return nil, nil, nil
		},
	}

	cfg := &config.Config{Workspace: workspace, Overlays: overlays, DatabaseURL: dsn, AutoApproverModel: "gpt-review", OpenAI: config.OpenAIConfig{APIKey: "test", APIBaseURL: "http://127.0.0.1:1"}}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() {
		done <- Run(ctx, cfg, "", slog.New(slog.DiscardHandler), assembler, sandbox.None{})
	}()

	ds.stop = sync.OnceFunc(func() {
		cancel()
		require.NoError(t, <-done)
	})
	t.Cleanup(ds.stop)

	select {
	case ds.rt = <-runtimes:
		return ds
	case err := <-done:
		t.Fatalf("Run returned before assembling: %v", err)
		return nil
	}
}

// agentDraftSaveWorkspace is a workspace whose agents/support-bot.md is supportBot, and a database for it.
func agentDraftSaveWorkspace(t *testing.T) (workspace, dsn string) {
	t.Helper()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	workspace = t.TempDir()
	writeAgentFile(t, workspace, "support-bot", supportBot)

	return workspace, dsn
}

func writeAgentFile(t *testing.T, dir, name, text string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", name+".md"), []byte(text), 0o600))
}

// agentFile reads dir's agents/<name>.md under the workspace, nil when there is none.
func (ds *agentDraftSave) agentFile(t *testing.T, dir, name string) *string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(ds.workspace, dir, "agents", name+".md"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	require.NoError(t, err)

	return new(string(data))
}

func (ds *agentDraftSave) save(t *testing.T, id string, reviewed ...string) (AgentDraftSaveState, error) {
	t.Helper()

	save := &AgentDraftSave{ID: id, Variant: "a", Destination: "workspace", Author: "alice"}
	if len(reviewed) > 0 {
		save.Reviewed, save.ReviewedFile = true, &reviewed[0]
	}

	return ds.rt.Sessions.SaveAgentDraft(t.Context(), ds.rt, save)
}

// refusal returns the save refusal err holds.
func refusal(t *testing.T, err error) *AgentDraftSaveError {
	t.Helper()

	errSave, ok := errors.AsType[*AgentDraftSaveError](err)
	require.True(t, ok, "%v", err)

	return errSave
}

func (ds *agentDraftSave) edit(t *testing.T, id string, op *AgentDraftOp) {
	t.Helper()

	draft, err := ds.rt.Sessions.LoadAgentDraft(t.Context(), id)
	require.NoError(t, err)

	op.BaseRevision, op.Author = draft.Revision, "alice"
	applyTestAgentDraftOp(t, ds.rt.Sessions, id, op)
}

func (ds *agentDraftSave) draft(t *testing.T, id string) *AgentDraft {
	t.Helper()

	draft, err := ds.rt.Sessions.LoadAgentDraft(t.Context(), id)
	require.NoError(t, err)

	return draft
}

// A save renames the draft's file into place, so a reader sees the old or the new file whole, and reloads
// it: the loaded file is the saved one, a later Reload keeps it, and the draft is live. One save at a time:
// another started meanwhile is refused. A renamed draft saves as a new agent and leaves the original alone.
func TestSaveAgentDraftGoesLive(t *testing.T) {
	t.Parallel()

	workspace, dsn := agentDraftSaveWorkspace(t)
	ds := startAgentDraftSave(t, workspace, dsn)
	draft, err := ds.rt.Sessions.CreateAgentDraft(t.Context(), "alice", AgentDraftOrigin{Name: "support-bot", Copy: true})
	require.NoError(t, err)
	ds.edit(t, draft.ID, &AgentDraftOp{Kind: AgentDraftEditBody, Variant: "a", Splices: []AgentDraftSplice{{Insert: "Kindly. "}}})

	old, err := os.Open(filepath.Join(ds.workspace, "agents", "support-bot.md"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, old.Close()) })

	validations := 0
	ds.validate = func(string) error {
		validations++
		if validations == 1 {
			_, errSecond := ds.save(t, draft.ID)
			require.EqualError(t, refusal(t, errSecond), "Someone is saving this draft already.")
		}

		return nil
	}

	state, err := ds.save(t, draft.ID)
	require.NoError(t, err)
	require.Equal(t, AgentDraftLive, state)
	require.Equal(t, 2, validations, "the save checks a stage holding the draft, then Reload checks its own")

	held, err := io.ReadAll(old)
	require.NoError(t, err)
	require.Equal(t, supportBot, string(held))

	want := "---\nmodel: gpt-6.1-sol\n---\nKindly. Help.\n"
	require.Equal(t, &want, ds.agentFile(t, "", "support-bot"))
	require.Equal(t, &want, ds.agentFile(t, config.DefaultRuntimeDir, "support-bot"))

	_, err = ds.rt.Reload("again")
	require.NoError(t, err)
	require.Equal(t, &want, ds.agentFile(t, config.DefaultRuntimeDir, "support-bot"))

	saved := ds.draft(t, draft.ID)
	require.Equal(t, AgentDraftLive, saved.SaveState)
	require.Equal(t, &want, saved.Bases[agentDraftWorkspaceLayer])

	ds.edit(t, draft.ID, &AgentDraftOp{Kind: AgentDraftRename, Value: "support-bot-2"})
	ds.edit(t, draft.ID, &AgentDraftOp{Kind: AgentDraftEditBody, Variant: "a", Splices: []AgentDraftSplice{{Insert: "Two. "}}})

	state, err = ds.save(t, draft.ID)
	require.NoError(t, err)
	require.Equal(t, AgentDraftLive, state)
	require.Equal(t, new("---\nmodel: gpt-6.1-sol\n---\nTwo. Kindly. Help.\n"), ds.agentFile(t, "", "support-bot-2"))
	require.Equal(t, &want, ds.agentFile(t, "", "support-bot"))
}

// A save writes nothing, and the draft goes back to editing, when the draft's file does not load, when the
// files Reload would load with it do not, or when its stored name, variant or destination is not one.
func TestSaveAgentDraftRefusesBeforeWriting(t *testing.T) {
	t.Parallel()

	workspace, dsn := agentDraftSaveWorkspace(t)
	ds := startAgentDraftSave(t, workspace, dsn)
	draft, err := ds.rt.Sessions.CreateAgentDraft(t.Context(), "alice", AgentDraftOrigin{Name: "support-bot", Copy: true})
	require.NoError(t, err)

	unchanged := func() {
		t.Helper()

		require.Equal(t, new(supportBot), ds.agentFile(t, "", "support-bot"))
		require.Equal(t, AgentDraftEditing, ds.draft(t, draft.ID).SaveState)
	}

	ds.edit(t, draft.ID, &AgentDraftOp{Kind: AgentDraftSetField, Variant: "a", Key: "sticky", Value: "sometimes"})

	_, err = ds.save(t, draft.ID)
	help, _ := agentSettingHelp("sticky", harness.AgentModeAdvanced)
	require.Equal(t, []AgentDraftProblem{{Variant: "a", Key: "sticky", Sentence: help.Allowed}}, refusal(t, err).Problems)
	unchanged()

	ds.edit(t, draft.ID, &AgentDraftOp{Kind: AgentDraftSetField, Variant: "a", Key: "sticky"})
	writeAgentFile(t, ds.workspace, "broken", "---\nsticky: sometimes\n---\n")

	_, err = ds.save(t, draft.ID)
	require.Contains(t, refusal(t, err).Problem, "broken.md")
	unchanged()
	require.NoError(t, os.Remove(filepath.Join(ds.workspace, "agents", "broken.md")))

	_, err = ds.rt.Sessions.SaveAgentDraft(t.Context(), ds.rt, &AgentDraftSave{ID: draft.ID, Variant: "zz", Destination: "workspace", Author: "alice"})
	require.EqualError(t, err, "That variant does not exist.")
	unchanged()

	_, err = ds.rt.Sessions.SaveAgentDraft(t.Context(), ds.rt, &AgentDraftSave{ID: draft.ID, Variant: "a", Destination: "github.com/acme/team", Author: "alice"})
	require.Equal(t, "That is not a save destination.", refusal(t, err).Problem)
	unchanged()

	_, err = ds.rt.Sessions.db.ExecContext(t.Context(), `UPDATE agent_drafts SET draft = jsonb_set(draft, '{name}', '"../x"') WHERE id = $1`, draft.ID)
	require.NoError(t, err)

	_, err = ds.save(t, draft.ID)
	errName, ok := errors.AsType[*AgentDraftNameError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, "../x", errName.Name)
	require.NoFileExists(t, filepath.Join(ds.workspace, "x.md"))
	unchanged()
}

// A save compares workspace agents/<name>.md itself with what the draft expects there, never the loaded
// copy: a change, even one nobody reloaded, and a file appearing where there was none, are reviewed setting
// by setting first. The person's picks are ordinary draft edits; the file they reviewed becomes what the next
// save expects, which compares again right before its rename.
func TestSaveAgentDraftReviewsChangedFile(t *testing.T) {
	t.Parallel()

	workspace, dsn := agentDraftSaveWorkspace(t)
	ds := startAgentDraftSave(t, workspace, dsn)
	draft, err := ds.rt.Sessions.CreateAgentDraft(t.Context(), "alice", AgentDraftOrigin{Name: "support-bot", Copy: true})
	require.NoError(t, err)
	ds.edit(t, draft.ID, &AgentDraftOp{Kind: AgentDraftSetField, Variant: "a", Key: "model", Value: "gpt-6-luna"})

	theirs := "---\nmodel: gpt-6.1-terra\n---\nHelp.\nCite sources.\n"
	writeAgentFile(t, ds.workspace, "support-bot", theirs)

	_, err = ds.save(t, draft.ID)
	review := refusal(t, err).Review
	require.NotNil(t, review)
	require.Equal(t, new(supportBot), review.Base)
	require.Equal(t, &theirs, review.Live)
	require.Equal(t, "---\nmodel: gpt-6-luna\n---\nHelp.\n", review.Draft)
	require.Equal(t, []AgentDraftReviewSetting{{Key: "model", Base: "gpt-6.1-sol", Live: "gpt-6.1-terra", Draft: "gpt-6-luna", Both: true}}, review.Settings)
	require.Equal(t, []AgentDraftSplice{{At: 6, Insert: "Cite sources.\n"}}, review.Splices)
	require.Equal(t, &theirs, ds.agentFile(t, "", "support-bot"))
	require.Equal(t, new(supportBot), ds.agentFile(t, config.DefaultRuntimeDir, "support-bot"), "nobody reloaded the edit")
	require.Equal(t, AgentDraftEditing, ds.draft(t, draft.ID).SaveState)

	// Take theirs on the new line, keep mine on model.
	applyTestAgentDraftOp(t, ds.rt.Sessions, draft.ID, &AgentDraftOp{BaseRevision: review.Revision, OpID: "alice-take", Author: "alice", Kind: AgentDraftEditBody, Variant: "a", Splices: review.Splices})

	newer := theirs + "Be brief.\n"
	writeAgentFile(t, ds.workspace, "support-bot", newer)

	_, err = ds.save(t, draft.ID, theirs)
	review = refusal(t, err).Review
	require.Equal(t, &theirs, review.Base)
	require.Equal(t, &newer, review.Live)
	require.Equal(t, &newer, ds.agentFile(t, "", "support-bot"))

	newest := newer + "Short.\n"
	ds.validate = func(string) error {
		writeAgentFile(t, ds.workspace, "support-bot", newest)
		ds.validate = func(string) error { return nil }

		return nil
	}

	_, err = ds.save(t, draft.ID, newer)
	require.Equal(t, &newest, refusal(t, err).Review.Live)
	require.Equal(t, &newest, ds.agentFile(t, "", "support-bot"))

	state, err := ds.save(t, draft.ID, newest)
	require.NoError(t, err)
	require.Equal(t, AgentDraftLive, state)
	require.Equal(t, new("---\nmodel: gpt-6-luna\n---\nHelp.\nCite sources.\n"), ds.agentFile(t, "", "support-bot"))

	ops, err := ds.rt.Sessions.AgentDraftOps(t.Context(), draft.ID, 0)
	require.NoError(t, err)

	taken := slices.IndexFunc(ops, func(op AgentDraftOp) bool { return op.OpID == "alice-take" })
	require.GreaterOrEqual(t, taken, 0, "History shows the take-theirs edit")
	require.Equal(t, "alice", ops[taken].Author)
	require.Equal(t, AgentDraftEditBody, ops[taken].Kind)

	// A new agent expects no file: it saves while there is none, and is reviewed once one appears.
	fresh, err := ds.rt.Sessions.CreateAgentDraft(t.Context(), "alice", AgentDraftOrigin{Name: "fresh-bot", Mode: harness.AgentModeAdvanced})
	require.NoError(t, err)
	ds.edit(t, fresh.ID, &AgentDraftOp{Kind: AgentDraftEditFile, Variant: "a", Value: "---\nmodel: gpt-6.1-sol\n---\nNew.\n"})

	appeared := "---\nmodel: gpt-6.1-sol\n---\nTheirs.\n"
	writeAgentFile(t, ds.workspace, "fresh-bot", appeared)

	_, err = ds.save(t, fresh.ID)
	review = refusal(t, err).Review
	require.Nil(t, review.Base)
	require.Equal(t, &appeared, review.Live)

	require.NoError(t, os.Remove(filepath.Join(ds.workspace, "agents", "fresh-bot.md")))

	state, err = ds.save(t, fresh.ID)
	require.NoError(t, err)
	require.Equal(t, AgentDraftLive, state)
}

// A draft is live only holding exactly what went live: edited while the save ran, or with another file
// loaded than the saved one, it goes back to editing, expecting the saved file at its destination.
func TestSaveAgentDraftLiveOnlyWithWhatItSaved(t *testing.T) {
	t.Parallel()

	workspace, dsn := agentDraftSaveWorkspace(t)
	ds := startAgentDraftSave(t, workspace, dsn)
	draft, err := ds.rt.Sessions.CreateAgentDraft(t.Context(), "alice", AgentDraftOrigin{Name: "support-bot", Copy: true})
	require.NoError(t, err)
	ds.edit(t, draft.ID, &AgentDraftOp{Kind: AgentDraftSetField, Variant: "a", Key: "model", Value: "gpt-6-luna"})

	// duringReload runs in Reload's validation, after the save renamed its file into place.
	duringReload := func(run func(stagedWorkDir string)) {
		validations := 0
		ds.validate = func(stagedWorkDir string) error {
			validations++
			if validations == 2 {
				run(stagedWorkDir)
			}

			return nil
		}
	}

	claimed := "---\nmodel: gpt-6-luna\n---\nHelp.\n"

	duringReload(func(string) {
		ds.edit(t, draft.ID, &AgentDraftOp{Kind: AgentDraftEditBody, Variant: "a", Splices: []AgentDraftSplice{{Insert: "Bob. "}}})
	})

	state, err := ds.save(t, draft.ID)
	require.NoError(t, err)
	require.Equal(t, AgentDraftEditing, state)
	require.Equal(t, &claimed, ds.agentFile(t, "", "support-bot"))
	require.Equal(t, &claimed, ds.agentFile(t, config.DefaultRuntimeDir, "support-bot"))

	edited := ds.draft(t, draft.ID)
	require.Equal(t, AgentDraftEditing, edited.SaveState)
	require.Equal(t, &claimed, edited.Bases[agentDraftWorkspaceLayer])
	require.Equal(t, "Bob. Help.\n", edited.Variants[0].Body)

	rewritten := "---\nmodel: gpt-6.1-terra\n---\nRewritten.\n"

	duringReload(func(stagedWorkDir string) {
		writeAgentFile(t, filepath.Join(ds.workspace, stagedWorkDir), "support-bot", rewritten)
	})

	saved := "---\nmodel: gpt-6-luna\n---\nBob. Help.\n"
	state, err = ds.save(t, draft.ID)
	require.NoError(t, err)
	require.Equal(t, AgentDraftEditing, state)
	require.Equal(t, &saved, ds.agentFile(t, "", "support-bot"))
	require.Equal(t, &rewritten, ds.agentFile(t, config.DefaultRuntimeDir, "support-bot"))
	require.Equal(t, &saved, ds.draft(t, draft.ID).Bases[agentDraftWorkspaceLayer])
	require.Equal(t, AgentDraftEditing, ds.draft(t, draft.ID).SaveState)
}

// A server stopped after a save renamed its valid file into place, before Reload, starts again on that file,
// and the draft it left saving is editing.
func TestAgentDraftSavingResetsOnStart(t *testing.T) {
	t.Parallel()

	workspace, dsn := agentDraftSaveWorkspace(t)
	ds := startAgentDraftSave(t, workspace, dsn)
	draft, err := ds.rt.Sessions.CreateAgentDraft(t.Context(), "alice", AgentDraftOrigin{Name: "support-bot", Copy: true})
	require.NoError(t, err)
	ds.edit(t, draft.ID, &AgentDraftOp{Kind: AgentDraftSetSaveState, Value: string(AgentDraftSaving)})

	swapped := "---\nmodel: gpt-6-luna\n---\nHelp.\n"
	writeAgentFile(t, workspace, "support-bot", swapped)
	ds.stop()

	ds = startAgentDraftSave(t, workspace, dsn)
	require.Equal(t, &swapped, ds.agentFile(t, config.DefaultRuntimeDir, "support-bot"))
	require.Equal(t, AgentDraftEditing, ds.draft(t, draft.ID).SaveState)
}

// The layers report says which layer provides a name and what a save to a destination replaces or is hidden
// by: any later file hides the destination, and only an earlier layer's own bytes at the destination still
// count as that layer's agent.
func TestAgentDraftLayers(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	store := newTestSessionServiceAt(t, workspace)
	// Overlays no Git reaches, so asking for their branches fails at once.
	overlays := []string{filepath.Join(workspace, "team.git") + "@main", filepath.Join(workspace, "more.git")}
	store.definitions = config.NewLockedConfig(&config.Config{Workspace: workspace, Overlays: overlays})
	team := skel.OverlayInfos(workspace, config.DefaultRuntimeDir, overlays)[0].ClonePath
	builtin := skel.BuiltinAgent("main")

	writeAgentFile(t, team, "triage", "team triage")
	writeAgentFile(t, workspace, "triage", "ours")
	writeAgentFile(t, workspace, "main", *builtin)

	type flags struct {
		hiddenBy, replaces string
		recreated          bool
	}

	layers := func(name, destination string) (*AgentDraftLayers, flags) {
		t.Helper()

		draft, err := store.CreateAgentDraft(t.Context(), "alice", AgentDraftOrigin{Name: name, Mode: harness.AgentModeAdvanced})
		require.NoError(t, err)

		got, err := store.AgentDraftLayers(t.Context(), draft.ID, "a", destination)
		require.NoError(t, err)
		require.Equal(t, "---\n{}\n---\n", got.Next)

		return got, flags{got.HiddenBy, got.Replaces, got.Recreated}
	}

	triage, got := layers("triage", overlays[0])
	require.Equal(t, flags{hiddenBy: "workspace"}, got)
	require.Equal(t, []AgentLayer{{Layer: "built-in"}, {Layer: overlays[0], Ref: "main", File: new("team triage")}, {Layer: overlays[1]}, {Layer: "workspace", File: new("ours")}}, triage.Layers)

	_, got = layers("triage", "workspace")
	require.Equal(t, flags{}, got, "the workspace file already replaces the overlay's")

	_, got = layers("main", "workspace")
	require.Equal(t, flags{replaces: "built-in"}, got, "a copy of the built-in is still the built-in")

	_, got = layers("main", overlays[1])
	require.Equal(t, flags{hiddenBy: "workspace", replaces: "built-in", recreated: true}, got)

	writeAgentFile(t, workspace, "main", "mine")

	_, got = layers("main", "workspace")
	require.Equal(t, flags{}, got)

	draft, err := store.CreateAgentDraft(t.Context(), "alice", AgentDraftOrigin{Name: "main", Mode: harness.AgentModeAdvanced})
	require.NoError(t, err)

	for _, destination := range []string{"built-in", "github.com/acme/other"} {
		_, err = store.AgentDraftLayers(t.Context(), draft.ID, "a", destination)
		require.EqualError(t, err, "That is not a save destination.")
	}

	_, err = store.AgentDraftLayers(t.Context(), draft.ID, "zz", "workspace")
	require.EqualError(t, err, "That variant does not exist.")
	_, err = store.AgentDraftLayers(t.Context(), "missing", "a", "workspace")
	require.ErrorIs(t, err, sql.ErrNoRows)
}
