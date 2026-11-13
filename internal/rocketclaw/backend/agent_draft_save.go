package backend

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
)

type agentDraftRuntime interface {
	Reload(reason string) (string, error)
	ValidateAgentFile(name string, data []byte) error
}

type AgentDraftSave struct {
	ID, Variant, Destination, Author string
	Reviewed, PullRequest            bool
	ReviewedFile                     *string
}

type AgentLayer struct {
	Layer, Ref string
	File       *string
}

type AgentDraftLayers struct {
	Layers                   []AgentLayer
	HiddenBy, Replaces       string
	Recreated                bool
	Next, Branch, Unpushable string
}

type AgentDraftReview struct {
	Revision   int64
	Base, Live *string
	Draft      string
	Settings   []AgentDraftReviewSetting
	Splices    []AgentDraftSplice
}

type AgentDraftReviewSetting struct {
	Key, Base, Live, Draft string
	Theirs, Both           bool
}

type AgentDraftSaveError struct {
	Problem  string
	Problems []AgentDraftProblem
	Review   *AgentDraftReview
}

func (e *AgentDraftSaveError) Error() string { return e.Problem }

func (s *SessionService) AgentDraftLayers(ctx context.Context, id, variant, destination string) (*AgentDraftLayers, error) {
	draft, err := s.LoadAgentDraft(ctx, id)
	if err != nil {
		return nil, err
	}

	i := draft.variant(variant)
	if i < 0 {
		return nil, &AgentDraftOpError{Problem: "That variant does not exist."}
	}

	_, bases, err := s.agentLayerFiles(draft.Name)
	if err != nil {
		return nil, err
	}

	cfg := s.definitions.Clone()
	layers := []AgentLayer{{Layer: agentDraftBuiltinLayer, File: skel.BuiltinAgent(draft.Name)}}
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

	ctx = context.WithoutCancel(ctx)

	draft, claimed, err := s.moveAgentDraft(ctx, save.ID, save.Author, AgentDraftSaving, expected, agentDraftClaim{})
	if err != nil {
		return "", err
	}

	if !claimed {
		return "", &AgentDraftSaveError{Problem: "Someone is saving this draft already."}
	}

	refuse := func(err error) (AgentDraftSaveState, error) {
		if _, _, errRelease := s.moveAgentDraft(ctx, draft.ID, save.Author, AgentDraftEditing, nil, agentDraftRelease{}); errRelease != nil {
			return "", errRelease
		}

		return "", err
	}

	i := draft.variant(save.Variant)

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

func (s *SessionService) reloadAgentDraft(ctx context.Context, rt agentDraftRuntime, draft *AgentDraft, author, destination, saved string, revision int64) (AgentDraftSaveState, error) {
	var loaded *string

	_, err := rt.Reload("save agent " + draft.Name)
	if err == nil {
		loaded, _, err = s.agentLayerFiles(draft.Name)
	}

	expected := map[string]*string{destination: &saved}

	_, live, errMove := s.moveAgentDraft(ctx, draft.ID, author, AgentDraftLive, expected, agentDraftLoaded{revision: revision, loaded: loaded, saved: &saved})
	if errMove == nil && !live {
		_, _, errMove = s.moveAgentDraft(ctx, draft.ID, author, AgentDraftEditing, expected, agentDraftStill{state: draft.SaveState, pending: revision})
	}

	if err = errors.Join(err, errMove); err != nil || !live {
		return AgentDraftEditing, err
	}

	return AgentDraftLive, nil
}

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

func (s *SessionService) resetSavingAgentDrafts(ctx context.Context) error {
	drafts, err := s.ListAgentDrafts(ctx)
	if err != nil {
		return err
	}

	for i := range drafts {
		if _, _, err := s.moveAgentDraft(ctx, drafts[i].ID, "rocketclaw", AgentDraftEditing, nil, agentDraftStill{state: AgentDraftSaving}); err != nil {
			return err
		}
	}

	return nil
}

type agentDraftMove interface {
	allows(now *AgentDraft) bool
}

type agentDraftClaim struct{}

func (agentDraftClaim) allows(now *AgentDraft) bool {
	return now.SaveState != AgentDraftSaving && now.SaveState != AgentDraftWaiting
}

type agentDraftRelease struct{}

func (agentDraftRelease) allows(*AgentDraft) bool { return true }

type agentDraftLoaded struct {
	revision      int64
	loaded, saved *string
}

func (m agentDraftLoaded) allows(now *AgentDraft) bool {
	return now.Revision == m.revision && sameAgentFile(m.loaded, m.saved)
}

type agentDraftStill struct {
	state   AgentDraftSaveState
	pending int64
	ended   AgentDraftWaitEnd
}

func (m agentDraftStill) allows(now *AgentDraft) bool {
	waiting := now.SaveState == AgentDraftWaiting
	if now.SaveState != m.state || waiting && m.pending != 0 && now.Pending.Revision != m.pending {
		return false
	}

	if waiting {
		now.Pending.Ended = m.ended
	}

	return true
}

type agentDraftWait struct{ pending *AgentDraftPending }

func (m agentDraftWait) allows(now *AgentDraft) bool {
	now.Pending = m.pending
	return true
}

func (s *SessionService) moveAgentDraft(ctx context.Context, id, author string, state AgentDraftSaveState, expected map[string]*string, move agentDraftMove) (*AgentDraft, bool, error) {
	for {
		draft, err := s.LoadAgentDraft(ctx, id)
		if err != nil || !move.allows(draft) {
			return draft, false, err
		}

		committed, err := s.commitAgentDraftOps(ctx, draft, []AgentDraftOp{{BaseRevision: draft.Revision, Author: author, Kind: AgentDraftSetSaveState, Value: string(state), Bases: expected}})
		if err != nil || committed {
			return draft, err == nil, err
		}
	}
}

func staleAgentDraft(draft *AgentDraft, variant *AgentDraftVariant, base, live *string) error {
	if sameAgentFile(live, base) {
		return nil
	}

	return &AgentDraftSaveError{Problem: "The agent's file changed since this draft began: review the changes before saving.", Review: reviewAgentDraft(draft, variant, base, live)}
}

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

func sameAgentFile(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
