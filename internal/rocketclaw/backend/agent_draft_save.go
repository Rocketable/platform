package backend

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
)

// agentDraftRuntime checks and reloads runtime assets, as Runtime does.
type agentDraftRuntime interface {
	Reload(reason string) (string, error)
	ValidateAgentFile(name string, data []byte) error
}

// AgentDraftSave asks to save Variant of draft ID to Destination for Author. Reviewed says Author reviewed
// the destination's file ReviewedFile, nil when it had none, which the save then expects to find there. To
// a Git overlay, PullRequest opens a pull request instead of pushing to the overlay's branch.
type AgentDraftSave struct {
	ID, Variant, Destination, Author string
	Reviewed, PullRequest            bool
	ReviewedFile                     *string
}

// AgentLayer is one layer's file for an agent name: Layer is "built-in", a configured overlay's spec, or
// "workspace"; Ref is an overlay's pinned ref; File is nil where the layer has none.
type AgentLayer struct {
	Layer, Ref string
	File       *string
}

// AgentDraftLayers is where saving a draft to a destination puts its agent: every layer in precedence
// order, each later one winning; HiddenBy, the later layer whose file hides the destination; Replaces, the
// earlier layer whose agent the save takes over; Recreated, that startup copies the built-in agent into
// workspace agents/ again, hiding an overlay destination; Next, the file the save writes; and for an overlay
// destination Branch, the branch a save pushes to, or Unpushable, why it cannot push there.
type AgentDraftLayers struct {
	Layers                   []AgentLayer
	HiddenBy, Replaces       string
	Recreated                bool
	Next, Branch, Unpushable string
}

// AgentDraftReview is a destination file that changed since the draft began, or since it was reviewed: the
// file the draft expected (Base), the file now (Live) and the draft's (Draft), nil where absent; the settings
// any of them changed; and Splices, the instructions edit on Revision that brings Live's changes into the
// draft's variant.
type AgentDraftReview struct {
	Revision   int64
	Base, Live *string
	Draft      string
	Settings   []AgentDraftReviewSetting
	Splices    []AgentDraftSplice
}

// AgentDraftReviewSetting is one setting's YAML in a review's base, live file and draft, empty where absent;
// mode is the setting "mode". Theirs says only the live file changed it, so the review starts on its value;
// Both, that the live file and the draft each changed it differently.
type AgentDraftReviewSetting struct {
	Key, Base, Live, Draft string
	Theirs, Both           bool
}

// AgentDraftSaveError reports a save that wrote nothing: Problem says why; Problems are the variant's
// settings that do not load; Review is the destination's change to review before saving again.
type AgentDraftSaveError struct {
	Problem  string
	Problems []AgentDraftProblem
	Review   *AgentDraftReview
}

func (e *AgentDraftSaveError) Error() string { return e.Problem }

// AgentDraftLayers reports, for draft id's name, each layer's file and what saving variant to destination
// would replace, be hidden by, and write.
func (s *SessionService) AgentDraftLayers(ctx context.Context, id, variant, destination string) (*AgentDraftLayers, error) {
	draft, err := s.LoadAgentDraft(ctx, id)
	if err != nil {
		return nil, err
	}

	i := slices.IndexFunc(draft.Variants, func(v AgentDraftVariant) bool { return v.Name == variant })
	if i < 0 {
		return nil, &AgentDraftOpError{Problem: "That variant does not exist."}
	}

	_, bases, err := s.agentLayerFiles(draft.Name)
	if err != nil {
		return nil, err
	}

	cfg := s.definitions.Clone()
	layers := []AgentLayer{{Layer: "built-in", File: skel.BuiltinAgent(draft.Name)}}
	overlays := skel.OverlayInfos("", cfg.RuntimeDirName(), cfg.Overlays)

	for _, overlay := range overlays {
		layers = append(layers, AgentLayer{Layer: overlay.Spec, Ref: overlay.Ref, File: bases[overlay.Spec]})
	}

	layers = append(layers, AgentLayer{Layer: agentDraftWorkspaceLayer, File: bases[agentDraftWorkspaceLayer]})

	at := slices.IndexFunc(layers[1:], func(layer AgentLayer) bool { return layer.Layer == destination }) + 1
	if at == 0 {
		return nil, &AgentDraftOpError{Problem: "That is not a save destination."}
	}

	report := &AgentDraftLayers{Layers: layers, Recreated: layers[0].File != nil && at < len(layers)-1, Next: string(renderAgentDraftFile(draft.Mode, &draft.Variants[i]))}

	// Any later file hides the destination; an earlier layer's agent is replaced only while the destination
	// holds none or that layer's own bytes, as startup's copy of a built-in does.
	var replaced AgentLayer

	for _, layer := range layers[at+1:] {
		if layer.File != nil {
			report.HiddenBy = layer.Layer
		}
	}

	for _, layer := range layers[:at] {
		if layer.File != nil {
			replaced = layer
		}
	}

	if replaced.File != nil && (layers[at].File == nil || sameAgentFile(layers[at].File, replaced.File)) {
		report.Replaces = replaced.Layer
	}

	if at < len(layers)-1 {
		if report.Branch, err = overlayBranch(ctx, overlays[at-1]); err != nil {
			report.Unpushable = err.Error()
		}
	}

	return report, nil
}

// SaveAgentDraft saves a variant of a draft to workspace agents/ and reloads, or to a Git overlay (see
// saveAgentDraftToOverlay), returning the draft's save state after: live, waiting for an overlay's merge, or
// editing when the draft changed during the save or Reload loaded another file. The save claims the draft,
// one save at a time; checks that the destination holds what the draft expects; checks the variant, then a
// stage of Reload's holding it; and renames its file into place after checking the destination again. A
// save that writes nothing returns the draft to editing and an *AgentDraftSaveError, or an error for a
// stored name or variant that is not one.
func (s *SessionService) SaveAgentDraft(ctx context.Context, rt agentDraftRuntime, save *AgentDraftSave) (AgentDraftSaveState, error) {
	cfg := s.definitions.Clone()
	overlays := skel.OverlayInfos("", cfg.RuntimeDirName(), cfg.Overlays)
	at := slices.IndexFunc(overlays, func(overlay skel.OverlayInfo) bool { return overlay.Spec == save.Destination })

	if save.Destination != agentDraftWorkspaceLayer && at < 0 {
		return "", &AgentDraftSaveError{Problem: "That is not a save destination."}
	}

	var expected map[string]*string
	if save.Reviewed {
		expected = map[string]*string{save.Destination: save.ReviewedFile}
	}

	draft, claimed, err := s.moveAgentDraft(ctx, save.ID, save.Author, AgentDraftSaving, expected, nil, func(draft *AgentDraft) bool {
		return draft.SaveState != AgentDraftSaving && draft.SaveState != AgentDraftWaiting
	})
	if err != nil {
		return "", err
	}

	if !claimed {
		return "", &AgentDraftSaveError{Problem: "Someone is saving this draft already."}
	}

	refuse := func(err error) (AgentDraftSaveState, error) {
		if _, _, errRelease := s.moveAgentDraft(ctx, draft.ID, save.Author, AgentDraftEditing, nil, nil, func(*AgentDraft) bool { return true }); errRelease != nil {
			return "", errRelease
		}

		return "", err
	}

	i := slices.IndexFunc(draft.Variants, func(variant AgentDraftVariant) bool { return variant.Name == save.Variant })

	switch {
	case !agentDraftNamePattern.MatchString(draft.Name):
		return refuse(&AgentDraftNameError{Name: draft.Name})
	case i < 0:
		return refuse(&AgentDraftOpError{Problem: "That variant does not exist."})
	case at >= 0:
		state, err := s.saveAgentDraftToOverlay(ctx, rt, save, draft, &draft.Variants[i], overlays[at])
		if state == "" {
			return refuse(err)
		}

		return state, err
	}

	variant, file := &draft.Variants[i], filepath.Join("agents", draft.Name+".md")
	data := renderAgentDraftFile(draft.Mode, variant)

	// changed reads the destination itself, never the loaded copy.
	changed := func() error {
		_, files, err := s.agentLayerFiles(draft.Name)
		if err != nil {
			return err
		}

		return staleAgentDraft(draft, variant, draft.Bases[agentDraftWorkspaceLayer], files[agentDraftWorkspaceLayer])
	}

	if err := changed(); err != nil {
		return refuse(err)
	}

	if err := s.checkAgentDraftVariant(rt, draft, variant); err != nil {
		return refuse(err)
	}

	root, err := os.OpenRoot(cfg.Workspace)
	if err != nil {
		return refuse(err)
	}

	defer func() { _ = root.Close() }()

	// The file is synced under a name of its own, then renamed into place, so a reader or a crash finds the
	// old file or the new one whole. A write between the last check and the rename is lost; that race is
	// accepted, as no lock covers the emulated shell.
	temp := ".rocketclaw-save-" + rand.Text()

	out, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err == nil {
		_, err = out.Write(data)
		err = errors.Join(err, out.Sync(), out.Close())
	}

	if err == nil {
		err = changed()
	}

	if err == nil {
		err = root.Rename(temp, file)
	}

	if err != nil {
		_ = root.Remove(temp)
		return refuse(err)
	}

	return s.reloadAgentDraft(ctx, rt, draft, save.Author, agentDraftWorkspaceLayer, string(data), draft.Revision)
}

// checkAgentDraftVariant refuses variant of draft when its settings do not load, or when Reload would not
// load the runtime files with it.
func (s *SessionService) checkAgentDraftVariant(rt agentDraftRuntime, draft *AgentDraft, variant *AgentDraftVariant) error {
	one := *draft
	one.Variants = []AgentDraftVariant{*variant}

	problems, err := s.ValidateAgentDraft(&one)

	switch {
	case err != nil:
		return err
	case len(problems) > 0:
		return &AgentDraftSaveError{Problem: "Fix the draft's settings before saving it.", Problems: problems}
	}

	if err := rt.ValidateAgentFile(draft.Name, renderAgentDraftFile(draft.Mode, variant)); err != nil {
		return &AgentDraftSaveError{Problem: "Saving it would keep RocketClaw from reloading: " + err.Error()}
	}

	return nil
}

// reloadAgentDraft runs Reload once saved went to destination, and moves draft, as of its save state, to
// live when it is still at revision and the loaded file is saved, else to editing; either way saved is what
// its next save expects at destination.
func (s *SessionService) reloadAgentDraft(ctx context.Context, rt agentDraftRuntime, draft *AgentDraft, author, destination, saved string, revision int64) (AgentDraftSaveState, error) {
	var loaded *string

	_, err := rt.Reload("save agent " + draft.Name)
	if err == nil {
		loaded, _, err = s.agentLayerFiles(draft.Name)
	}

	expected := map[string]*string{destination: &saved}

	_, live, errMove := s.moveAgentDraft(ctx, draft.ID, author, AgentDraftLive, expected, nil, func(now *AgentDraft) bool {
		return now.Revision == revision && sameAgentFile(loaded, &saved)
	})
	if errMove == nil && !live {
		_, _, errMove = s.moveAgentDraft(ctx, draft.ID, author, AgentDraftEditing, expected, nil, func(now *AgentDraft) bool { return now.SaveState == draft.SaveState })
	}

	if err = errors.Join(err, errMove); err != nil || !live {
		return AgentDraftEditing, err
	}

	return AgentDraftLive, nil
}

// ValidateAgentFile runs Reload's validation on a stage built as Reload builds one, holding data as
// workspace agents/<name>.md.
func (r *Runtime) ValidateAgentFile(name string, data []byte) error {
	cfg := r.Cfg.Clone()

	if err := skel.ValidateRuntimeAssetsWith(cfg.Workspace, cfg.Overlays, r.Log, map[string][]byte{path.Join("agents", name+".md"): data}, func(runtimeDir string) error {
		if err := validateRuntimeAssets(r.Cfg, runtimeDir); err != nil {
			return err
		}

		return r.assemble.ValidateAssets(cfg, runtimeDir)
	}); err != nil {
		return fmt.Errorf("validate runtime assets with agent %q: %w", name, err)
	}

	return nil
}

// resetSavingAgentDrafts returns each draft a stopped server left saving to editing.
func (s *SessionService) resetSavingAgentDrafts(ctx context.Context) error {
	drafts, err := s.ListAgentDrafts(ctx)

	for i := range drafts {
		if err == nil {
			_, _, err = s.moveAgentDraft(ctx, drafts[i].ID, "rocketclaw", AgentDraftEditing, nil, nil, func(draft *AgentDraft) bool { return draft.SaveState == AgentDraftSaving })
		}
	}

	return err
}

// moveAgentDraft commits on draft id, once allowed holds of the draft as read, a set_save_state op by
// author to state that makes expected its destinations' expected files and, unless nil, pending its pending
// save; the op commits only on the revision read, else the draft is read again. It returns the draft as
// moved, or false when allowed failed.
func (s *SessionService) moveAgentDraft(ctx context.Context, id, author string, state AgentDraftSaveState, expected map[string]*string, pending *AgentDraftPending, allowed func(*AgentDraft) bool) (*AgentDraft, bool, error) {
	for {
		draft, err := s.LoadAgentDraft(ctx, id)
		if err != nil || !allowed(draft) {
			return draft, false, err
		}

		op := AgentDraftOp{BaseRevision: draft.Revision, Author: author, Kind: AgentDraftSetSaveState, Value: string(state), Bases: expected}
		draft.Revision, draft.SaveState, draft.Bases = draft.Revision+1, state, maps.Clone(draft.Bases)
		maps.Copy(draft.Bases, expected)
		draft.Pending = cmp.Or(pending, draft.Pending)

		doc, _ := json.Marshal(draft) // Strings, maps and slices of them always encode.
		opJSON, _ := json.Marshal(op)

		committed, err := execRows(ctx, s.db, "move agent draft", `WITH changed AS (
    UPDATE agent_drafts SET revision = $2 + 1, draft = $3 WHERE id = $1 AND revision = $2 RETURNING id
), logged AS (
    INSERT INTO agent_draft_ops (draft_id, revision, op, draft) SELECT id, $2 + 1, $4, $3 FROM changed
)
SELECT pg_notify('rocketclaw_transcript_' || md5(current_schema()), json_build_object('conversationId', 'draft:' || id, 'revision', pg_current_xact_id()::text)::text) FROM changed`,
			id, op.BaseRevision, string(doc), string(opJSON))
		if err != nil || committed > 0 {
			return draft, err == nil, err
		}
	}
}

// staleAgentDraft asks for a review when live, the file at the destination of a save of variant of draft, is
// not base, the file the draft expects there.
func staleAgentDraft(draft *AgentDraft, variant *AgentDraftVariant, base, live *string) error {
	if sameAgentFile(live, base) {
		return nil
	}

	return &AgentDraftSaveError{Problem: "The agent's file changed since this draft began: review the changes before saving.", Review: reviewAgentDraft(draft, variant, base, live)}
}

// reviewAgentDraft compares the destination's expected file base, its file live, and variant of draft,
// setting by setting, and moves live's instructions changes from base past the variant's own. A file that
// does not parse compares as having no settings and no instructions.
func reviewAgentDraft(draft *AgentDraft, variant *AgentDraftVariant, base, live *string) *AgentDraftReview {
	review := &AgentDraftReview{Revision: draft.Revision, Base: base, Live: live, Draft: string(renderAgentDraftFile(draft.Mode, variant))}
	sides, modes := [3]AgentDraftVariant{2: *variant}, [3]harness.AgentMode{2: draft.Mode}

	for i, file := range []*string{base, live} {
		if file != nil {
			modes[i], sides[i], _ = parseAgentDraftFile(*file)
		}
	}

	var keys []string

	for i := range sides {
		sides[i].Settings = append([]AgentDraftSetting{{Key: "mode", Value: string(modes[i])}}, sides[i].Settings...)
		for _, setting := range sides[i].Settings {
			if !slices.Contains(keys, setting.Key) {
				keys = append(keys, setting.Key)
			}
		}
	}

	value := func(side int, key string) string {
		if j := slices.IndexFunc(sides[side].Settings, func(setting AgentDraftSetting) bool { return setting.Key == key }); j >= 0 {
			return sides[side].Settings[j].Value
		}

		return ""
	}

	for _, key := range keys {
		if b, l, d := value(0, key), value(1, key), value(2, key); l != b || d != b {
			review.Settings = append(review.Settings, AgentDraftReviewSetting{Key: key, Base: b, Live: l, Draft: d, Theirs: l != b && d == b, Both: l != b && d != b && l != d})
		}
	}

	review.Splices, _ = transformAgentDraftSplices(diffAgentDraftBody(sides[0].Body, sides[1].Body), diffAgentDraftBody(sides[0].Body, sides[2].Body), false)

	return review
}

// sameAgentFile reports whether a and b are both absent or hold the same text.
func sameAgentFile(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
