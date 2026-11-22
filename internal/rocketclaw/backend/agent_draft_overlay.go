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

const agentDraftBranchPrefix = "rocketclaw-agent-draft-"

type AgentDraftPending struct {
	Destination string            `json:"destination"`
	Base        string            `json:"base"`
	Branch      string            `json:"branch"`
	Commit      string            `json:"commit"`
	Number      int               `json:"number,omitempty"`
	Link        string            `json:"link,omitempty"`
	File        string            `json:"file"`
	Revision    int64             `json:"revision"`
	Ended       AgentDraftWaitEnd `json:"ended,omitempty"`
}

type AgentDraftWaitEnd string

const (
	AgentDraftWaitClosed  AgentDraftWaitEnd = "closed"
	AgentDraftWaitStopped AgentDraftWaitEnd = "stopped"
	AgentDraftWaitEdited  AgentDraftWaitEnd = "edited"
)

func (s *SessionService) CheckAgentDraftMerge(ctx context.Context, rt agentDraftRuntime, id, author string) (AgentDraftSaveState, error) {
	draft, err := s.LoadAgentDraft(ctx, id)
	if err != nil || draft.SaveState != AgentDraftWaiting || draft.Pending == nil {
		return draft.SaveState, err
	}

	pending, cfg := draft.Pending, s.definitions.Clone()
	overlays := skel.OverlayInfos(cfg.RuntimeDirName(), cfg.Overlays)
	at := slices.IndexFunc(overlays, func(overlay skel.OverlayInfo) bool { return overlay.Spec == pending.Destination })

	var state string
	if pending.Number > 0 {
		state, _ = agentDraftCommand(ctx, "", "gh", "pr", "view", pending.Link, "--json", "state", "--jq", ".state")
		state = strings.TrimSpace(state)
	}

	switch {
	case state == "CLOSED":
		draft, _, err = s.moveAgentDraft(ctx, id, author, AgentDraftEditing, nil, agentDraftStill{state: AgentDraftWaiting, pending: pending.Revision, ended: AgentDraftWaitClosed})
		return draft.SaveState, err
	case state == "" && at >= 0:
		dir, err := os.MkdirTemp(filepath.Join(cfg.Workspace, cfg.RuntimeDirName()), "agent-save-*")
		if err != nil {
			return "", fmt.Errorf("create agent save stage: %w", err)
		}

		defer func() { _ = os.RemoveAll(dir) }()

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

func (s *SessionService) StopAgentDraftWaiting(ctx context.Context, id, author string, reason AgentDraftWaitEnd) (AgentDraftSaveState, error) {
	draft, _, err := s.moveAgentDraft(ctx, id, author, AgentDraftEditing, nil, agentDraftStill{state: AgentDraftWaiting, ended: reason})

	return draft.SaveState, err
}

func (s *SessionService) saveAgentDraftToOverlay(ctx context.Context, rt agentDraftRuntime, save *AgentDraftSave, draft *AgentDraft, variant *AgentDraftVariant, overlay skel.OverlayInfo) (AgentDraftSaveState, error) {
	branch, err := overlayBranch(ctx, overlay)
	if err != nil {
		return "", err
	}

	pending := &AgentDraftPending{Destination: overlay.Spec, Base: branch, Branch: branch, File: renderAgentDraftFile(draft.Mode, variant), Revision: draft.Revision + 1}
	data, file := []byte(pending.File), path.Join("agents", draft.Name+".md")

	if save.PullRequest {
		pending.Branch = fmt.Sprintf("%s%s-%d", agentDraftBranchPrefix, draft.ID, draft.Revision)
		if repo, ok := strings.CutPrefix(strings.TrimSuffix(overlay.URL, ".git"), "https://github.com/"); ok {
			pending.Link = "https://github.com/" + repo + "/compare/" + branch + "..." + pending.Branch + "?expand=1"
		}
	}

	definitions := s.definitions.Clone()

	stage, err := os.MkdirTemp(filepath.Join(definitions.Workspace, definitions.RuntimeDirName()), "agent-save-*")
	if err != nil {
		return "", fmt.Errorf("create agent save stage: %w", err)
	}

	defer func() { _ = os.RemoveAll(stage) }()

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

		if err := staleAgentDraft(draft, variant, data, draft.Bases[overlay.Spec], live); err != nil {
			return false, err
		}

		if err := s.checkAgentDraftVariant(rt, draft, variant, data); err != nil {
			return false, err
		}

		unchanged := sameAgentFile(live, &pending.File)
		if unchanged && save.PullRequest {
			return false, &AgentDraftSaveError{Problem: "Branch " + branch + " of " + overlay.Spec + " already has this file, so a pull request would have nothing to commit."}
		}

		if err := errors.Join(root.MkdirAll("agents", 0o755), root.WriteFile(file, data, 0o644)); err != nil {
			return false, fmt.Errorf("write staged agent: %w", err)
		}

		staged, err := agentDraftCommand(ctx, dir, "git", "add", "--", file)
		if err == nil {
			staged, err = agentDraftCommand(ctx, dir, "git", "cat-file", "blob", ":"+file)
		}

		if err == nil && staged != pending.File {
			err = &AgentDraftSaveError{Problem: "Git would commit other bytes than the checked file, so nothing was pushed."}
		}

		if err == nil && !unchanged {
			_, err = agentDraftCommand(ctx, dir, "git", "-c", "user.name="+save.Author, "-c", "user.email="+save.Author, "commit", "-m", "Save agent "+draft.Name+" from the agent editor")
		}

		if err == nil {
			pending.Commit, err = agentDraftCommand(ctx, dir, "git", "rev-parse", "HEAD")
			pending.Commit = strings.TrimSpace(pending.Commit)
		}

		if err != nil || unchanged {
			return false, err
		}

		out, err := agentDraftCommand(ctx, dir, "git", "push", "--porcelain", "origin", "HEAD:refs/heads/"+pending.Branch)

		return err != nil && strings.Contains(out, "[rejected]"), err
	}

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
			pending.Number, _ = strconv.Atoi(path.Base(pending.Link))
		}
	}

	if _, _, err := s.moveAgentDraft(ctx, draft.ID, save.Author, AgentDraftWaiting, nil, agentDraftWait{pending: pending}); err != nil {
		return "", err
	}

	state, err := s.CheckAgentDraftMerge(ctx, rt, draft.ID, save.Author)

	return cmp.Or(state, AgentDraftWaiting), err
}

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
