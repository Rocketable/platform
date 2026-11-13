package backend

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/skel"
)

// agentDraftBranchPrefix starts the branch of each pull request a save opens, followed by the draft's id
// and the save's claim revision, so no save ever pushes over an open pull request's branch.
const agentDraftBranchPrefix = "rocketclaw-agent-draft-"

// AgentDraftPending is a draft's save pushed to the Git overlay Destination: Commit, holding File, on
// Branch, which is Base, the branch the overlay saves to, or a branch of the save's own for a pull request
// into Base. Number is the pull request gh opened, and Link its page or, without gh, a GitHub page that
// compares Branch with Base. Revision is the draft's once it waits for the merge.
type AgentDraftPending struct {
	Destination string `json:"destination"`
	Base        string `json:"base"`
	Branch      string `json:"branch"`
	Commit      string `json:"commit"`
	Number      int    `json:"number,omitempty"`
	Link        string `json:"link,omitempty"`
	File        string `json:"file"`
	Revision    int64  `json:"revision"`
}

// CheckAgentDraftMerge follows draft id's save to a Git overlay while the draft waits for its merge: once
// the overlay's branch holds the commit, or gh reports the pull request merged, Reload runs and the draft
// goes live as a workspace save does, as author; a pull request gh reports closed returns it to editing. It
// returns the draft's save state, still waiting while the merge cannot be told, as after a squash merge
// without gh.
func (s *SessionService) CheckAgentDraftMerge(ctx context.Context, rt agentDraftRuntime, id, author string) (AgentDraftSaveState, error) {
	draft, err := s.LoadAgentDraft(ctx, id)
	if err != nil || draft.SaveState != AgentDraftWaiting || draft.Pending == nil {
		return draft.SaveState, err
	}

	pending, cfg := draft.Pending, s.definitions.Clone()
	overlays := skel.OverlayInfos("", cfg.RuntimeDirName(), cfg.Overlays)
	at := slices.IndexFunc(overlays, func(overlay skel.OverlayInfo) bool { return overlay.Spec == pending.Destination })

	var state string
	if pending.Number > 0 {
		state, _ = agentDraftCommand(ctx, "", "gh", "pr", "view", pending.Link, "--json", "state", "--jq", ".state") // Without gh, the branch tells.
		state = strings.TrimSpace(state)
	}

	switch {
	case state == "CLOSED":
		return s.StopAgentDraftWaiting(ctx, id, author)
	case state == "" && at >= 0:
		dir, err := os.MkdirTemp(cfg.Workspace, ".rocketclaw-save-*")
		if err != nil {
			return "", fmt.Errorf("create agent save stage: %w", err)
		}

		defer func() { _ = os.RemoveAll(dir) }()

		// The runtime's clone of the overlay holds one commit, too little history to tell.
		if _, err := agentDraftCommand(ctx, dir, "git", "clone", "--bare", "--filter=blob:none", "--single-branch", "--branch", pending.Base, "--", overlays[at].URL, "."); err != nil {
			return "", err
		}

		if _, errAncestor := agentDraftCommand(ctx, dir, "git", "merge-base", "--is-ancestor", pending.Commit, "HEAD"); errAncestor == nil {
			state = "MERGED"
		}
	}

	if state != "MERGED" {
		return AgentDraftWaiting, nil
	}

	return s.reloadAgentDraft(ctx, rt, draft, author, pending.Destination, pending.File, pending.Revision)
}

// StopAgentDraftWaiting returns draft id, when it waits for a merge, to editing as author. Its pull request
// stays open, and its pending save stays on the draft to show. It returns the draft's save state.
func (s *SessionService) StopAgentDraftWaiting(ctx context.Context, id, author string) (AgentDraftSaveState, error) {
	draft, _, err := s.moveAgentDraft(ctx, id, author, AgentDraftEditing, nil, nil, func(now *AgentDraft) bool { return now.SaveState == AgentDraftWaiting })

	return draft.SaveState, err
}

// saveAgentDraftToOverlay commits variant's file of draft, claimed by save, in a clone of the branch overlay
// saves to, as the saving person, and pushes it there or, for a pull request, to a branch of the save's own,
// opening the pull request with gh when gh is logged in. Nothing is forced: a push the branch moved under
// starts again from a new clone, so the destination is checked again. The draft then waits for the merge,
// which a push to the branch already is. It returns "" when no pull request or push is waiting.
func (s *SessionService) saveAgentDraftToOverlay(ctx context.Context, rt agentDraftRuntime, save *AgentDraftSave, draft *AgentDraft, variant *AgentDraftVariant, overlay skel.OverlayInfo) (AgentDraftSaveState, error) {
	branch, err := overlayBranch(ctx, overlay)
	if err != nil {
		return "", err
	}

	data, file := renderAgentDraftFile(draft.Mode, variant), path.Join("agents", draft.Name+".md")
	pending := &AgentDraftPending{Destination: overlay.Spec, Base: branch, Branch: branch, File: string(data), Revision: draft.Revision + 1}

	if save.PullRequest {
		pending.Branch = fmt.Sprintf("%s%s-%d", agentDraftBranchPrefix, draft.ID, draft.Revision)
		if repo, ok := strings.CutPrefix(strings.TrimSuffix(overlay.URL, ".git"), "https://github.com/"); ok {
			pending.Link = "https://github.com/" + repo + "/compare/" + branch + "..." + pending.Branch + "?expand=1"
		}
	}

	stage, err := os.MkdirTemp(s.definitions.Clone().Workspace, ".rocketclaw-save-*")
	if err != nil {
		return "", fmt.Errorf("create agent save stage: %w", err)
	}

	defer func() { _ = os.RemoveAll(stage) }()

	// push clones the branch into dir, checks the file there and the draft, commits exactly the checked bytes
	// and pushes them, and tells whether the branch moved under the push.
	push := func(dir string) (bool, error) {
		if _, err := agentDraftCommand(ctx, stage, "git", "clone", "--depth=1", "--single-branch", "--branch", branch, "--", overlay.URL, dir); err != nil {
			return false, err
		}

		root, err := os.OpenRoot(dir)
		if err != nil {
			return false, fmt.Errorf("open agent save stage: %w", err)
		}

		defer func() { _ = root.Close() }()

		var live *string

		text, err := root.ReadFile(file)
		if err == nil {
			live = new(string(text))
		} else if !errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("read staged agent: %w", err)
		}

		if err := staleAgentDraft(draft, variant, draft.Bases[overlay.Spec], live); err != nil {
			return false, err
		}

		if err := s.checkAgentDraftVariant(rt, draft, variant); err != nil {
			return false, err
		}

		if err := errors.Join(root.MkdirAll("agents", 0o755), root.WriteFile(file, data, 0o644)); err != nil {
			return false, fmt.Errorf("write staged agent: %w", err)
		}

		staged, err := agentDraftCommand(ctx, dir, "git", "add", "--", file)
		if err == nil {
			staged, err = agentDraftCommand(ctx, dir, "git", "cat-file", "blob", ":"+file)
		}

		if err == nil && staged != string(data) {
			err = &AgentDraftSaveError{Problem: "Git would commit other bytes than the checked file, so nothing was pushed."}
		}

		if err == nil {
			_, err = agentDraftCommand(ctx, dir, "git", "-c", "user.name="+save.Author, "-c", "user.email="+save.Author, "commit", "-m", "Save agent "+draft.Name+" from the agent editor")
		}

		if err == nil {
			pending.Commit, err = agentDraftCommand(ctx, dir, "git", "rev-parse", "HEAD")
			pending.Commit = strings.TrimSpace(pending.Commit)
		}

		if err != nil {
			return false, err
		}

		out, err := agentDraftCommand(ctx, dir, "git", "push", "--porcelain", "origin", "HEAD:refs/heads/"+pending.Branch)

		return err != nil && strings.Contains(out, "[rejected]"), err
	}

	// ponytail: a branch that moves under every push keeps the save cloning again; a cap on tries would
	// bound it.
	var dir string

	for n, moved := 0, true; moved; n++ {
		dir = filepath.Join(stage, strconv.Itoa(n))

		if moved, err = push(dir); err != nil && !moved {
			return "", err
		}
	}

	if save.PullRequest {
		if _, errGh := agentDraftCommand(ctx, dir, "gh", "auth", "status"); errGh == nil {
			if pending.Link, err = agentDraftCommand(ctx, dir, "gh", "pr", "create", "--base", branch, "--head", pending.Branch, "--title", "Save agent "+draft.Name, "--body", "Saved from the RocketClaw agent editor by "+save.Author+"."); err != nil {
				return "", err
			}

			pending.Link = strings.TrimSpace(pending.Link)
			pending.Number, _ = strconv.Atoi(path.Base(pending.Link)) // gh prints the pull request's page, which ends in its number.
		}
	}

	if _, _, err := s.moveAgentDraft(ctx, draft.ID, save.Author, AgentDraftWaiting, nil, pending, func(*AgentDraft) bool { return true }); err != nil {
		return "", err
	}

	state, err := s.CheckAgentDraftMerge(ctx, rt, draft.ID, save.Author)

	return cmp.Or(state, AgentDraftWaiting), err
}

// overlayBranch returns the branch saves to overlay push to: the ref it pins, when that is a branch, or the
// remote's default branch when it pins none.
func overlayBranch(ctx context.Context, overlay skel.OverlayInfo) (string, error) {
	if overlay.Ref == "" {
		out, err := agentDraftCommand(ctx, "", "git", "ls-remote", "--symref", "--", overlay.URL, "HEAD")
		head, _, _ := strings.Cut(out, "\t")

		return strings.TrimPrefix(head, "ref: refs/heads/"), err
	}

	out, err := agentDraftCommand(ctx, "", "git", "ls-remote", "--heads", "--", overlay.URL, "refs/heads/"+overlay.Ref)
	if err == nil && out == "" {
		err = &AgentDraftSaveError{Problem: overlay.Spec + " is pinned to a tag or commit, so a save cannot push to it; pin it to a branch to save there."}
	}

	if err != nil {
		return "", err
	}

	return overlay.Ref, nil
}

// agentDraftCommand runs name, git or gh, with args in dir, never prompting, and returns its output. Git
// runs no hooks and no file system monitor, so nothing a clone holds runs.
func agentDraftCommand(ctx context.Context, dir, name string, args ...string) (string, error) {
	if name == "git" {
		args = append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)
	}

	cmd := exec.CommandContext(ctx, name, args...)

	cmd.Dir, cmd.Env = dir, append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	out, err := cmd.Output()
	if errExit, ok := errors.AsType[*exec.ExitError](err); ok {
		err = fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, bytes.TrimSpace(errExit.Stderr))
	}

	return string(out), err
}
