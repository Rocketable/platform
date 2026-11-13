package backend

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
	"github.com/stretchr/testify/require"
)

const (
	triageFile  = "---\nmodel: gpt-6.1-sol\n---\nTriage.\n"
	teamOverlay = "github.com/acme/team"
)

// agentDraftOverlay is the bare repository every overlay of a server reaches, and the files where its fake
// gh logs the pull requests it opens and reads the state it reports for them.
type agentDraftOverlay struct {
	ds                     *agentDraftSave
	remote, created, state string
}

// startAgentDraftOverlay runs a server whose overlays are github.com/acme/team pinned as pins say, each
// reaching, through Git's insteadOf, one local bare repository whose default branch, trunk, holds
// agents/triage.md. PATH holds only git and, with gh, a fake gh that is logged in.
func startAgentDraftOverlay(t *testing.T, gh bool, pins ...string) *agentDraftOverlay {
	t.Helper()

	dir := t.TempDir()
	o := &agentDraftOverlay{remote: filepath.Join(dir, "team.git"), created: filepath.Join(dir, "created"), state: filepath.Join(dir, "state")}
	testGit(t, dir, "init", "--bare", "-b", "trunk", o.remote)

	seed := filepath.Join(dir, "seed")
	testGit(t, dir, "init", "-b", "trunk", seed)
	writeAgentFile(t, seed, "triage", triageFile)
	testGit(t, seed, "add", "-A")
	testGit(t, seed, "commit", "-m", "seed")
	testGit(t, seed, "tag", "v1")
	testGit(t, seed, "push", o.remote, "trunk", "v1")

	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.Mkdir(bin, 0o755))

	git, err := exec.LookPath("git")
	require.NoError(t, err)
	require.NoError(t, os.Symlink(git, filepath.Join(bin, "git")))

	if gh {
		script := "#!/bin/sh\ncase \"$1 $2\" in\n\"pr create\") echo \"$@\" >> " + o.created + "; echo https://github.com/acme/team/pull/7;;\n\"pr view\") read -r state < " + o.state + "; echo \"$state\";;\nesac\n"
		require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o700))
	}

	t.Setenv("PATH", bin)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+o.remote+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://"+teamOverlay)

	overlays := make([]string, 0, len(pins))
	for _, pin := range pins {
		overlays = append(overlays, teamOverlay+pin)
	}

	workspace, dsn := agentDraftSaveWorkspace(t)
	o.ds = startAgentDraftSave(t, workspace, dsn, overlays...)

	return o
}

func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", append([]string{"-c", "user.name=teammate", "-c", "user.email=teammate"}, args...)...)
	cmd.Dir = dir

	out, err := cmd.Output()
	if errExit, ok := errors.AsType[*exec.ExitError](err); ok {
		require.NoError(t, err, "git %v: %s", args, errExit.Stderr)
	}

	require.NoError(t, err)

	return string(out)
}

// head returns the commit the remote's branch points at, empty when there is no such branch.
func (o *agentDraftOverlay) head(t *testing.T, branch string) string {
	t.Helper()

	head, _, _ := strings.Cut(testGit(t, o.remote, "ls-remote", "--heads", o.remote, "refs/heads/"+branch), "\t")

	return head
}

// teammate pushes to the remote's trunk what change makes in a fresh clone of it.
func (o *agentDraftOverlay) teammate(t *testing.T, change func(dir string)) {
	t.Helper()

	dir := t.TempDir()
	testGit(t, dir, "clone", "--branch", "trunk", o.remote, ".")
	change(dir)
	testGit(t, dir, "add", "-A")
	testGit(t, dir, "commit", "--allow-empty", "-m", "teammate")
	testGit(t, dir, "push", "origin", "HEAD:trunk")
}

// copyDraft starts a draft of the effective triage agent, with "Kindly. " put before its instructions.
func (o *agentDraftOverlay) copyDraft(t *testing.T) string {
	t.Helper()

	draft, err := o.ds.rt.Sessions.CreateAgentDraft(t.Context(), "alice", AgentDraftOrigin{Name: "triage", Copy: true})
	require.NoError(t, err)
	o.ds.edit(t, draft.ID, &AgentDraftOp{Kind: AgentDraftEditBody, Variant: "a", Splices: []AgentDraftSplice{{Insert: "Kindly. "}}})

	return draft.ID
}

func (o *agentDraftOverlay) save(t *testing.T, id, destination string, pullRequest bool) (AgentDraftSaveState, error) {
	t.Helper()

	return o.ds.rt.Sessions.SaveAgentDraft(t.Context(), o.ds.rt, &AgentDraftSave{ID: id, Variant: "a", Destination: destination, Author: "alice", PullRequest: pullRequest})
}

func (o *agentDraftOverlay) merged(t *testing.T, id string) AgentDraftSaveState {
	t.Helper()

	state, err := o.ds.rt.Sessions.CheckAgentDraftMerge(t.Context(), o.ds.rt, id, "bob")
	require.NoError(t, err)
	require.Equal(t, state, o.ds.draft(t, id).SaveState)

	return state
}

// The runtime's clone of the overlay holds only the commit it was pinned at.
func (o *agentDraftOverlay) requireShallowRuntimeClone(t *testing.T) {
	t.Helper()

	cfg := o.ds.rt.Cfg.Clone()
	require.FileExists(t, filepath.Join(skel.OverlayInfos(cfg.Workspace, cfg.RuntimeDirName(), cfg.Overlays)[0].ClonePath, ".git", "shallow"))
}

// Pushing commits the draft's file in a clone of the pinned branch, as the person, and goes live once the
// branch holds the commit and Reload loaded it. Git hooks never run in that clone, and nothing is forced: a
// push the branch moved under is tried again on a new clone, which reviews a change to the agent's file.
func TestSaveAgentDraftPushesToOverlay(t *testing.T) {
	o := startAgentDraftOverlay(t, false, "@trunk")
	spec := teamOverlay + "@trunk"
	id := o.copyDraft(t)
	marker := filepath.Join(t.TempDir(), "hook-ran")

	// moveBranch runs in the first check of a save's stage, after the clone and before the push.
	moveBranch := func(change func(dir string)) {
		o.ds.validate = func(string) error {
			o.ds.validate = func(string) error { return nil }

			hooks, err := filepath.Glob(filepath.Join(o.ds.workspace, config.DefaultRuntimeDir, "agent-save-*", "*", ".git", "hooks"))
			require.NoError(t, err)
			require.Len(t, hooks, 1)
			require.NoError(t, os.WriteFile(filepath.Join(hooks[0], "pre-commit"), []byte("#!/bin/sh\n: > "+marker+"\n"), 0o700))

			o.teammate(t, change)

			return nil
		}
	}

	moveBranch(func(dir string) { writeAgentFile(t, dir, "other", triageFile) })

	state, err := o.save(t, id, spec, false)
	require.NoError(t, err)
	require.Equal(t, AgentDraftLive, state)

	saved := "---\nmodel: gpt-6.1-sol\n---\nKindly. Triage.\n"
	require.Equal(t, saved, testGit(t, o.remote, "show", "trunk:agents/triage.md"))
	require.Equal(t, triageFile, testGit(t, o.remote, "show", "trunk:agents/other.md"), "the teammate's commit stays")
	require.Equal(t, "alice <alice>\n", testGit(t, o.remote, "log", "-1", "--format=%an <%ae>", "trunk"))
	require.NoFileExists(t, marker)
	require.Equal(t, &saved, o.ds.agentFile(t, config.DefaultRuntimeDir, "triage"))
	o.requireShallowRuntimeClone(t)

	draft := o.ds.draft(t, id)
	require.Equal(t, &saved, draft.Bases[spec])
	require.Equal(t, [5]string{spec, "trunk", "trunk", o.head(t, "trunk"), saved}, [5]string{draft.Pending.Destination, draft.Pending.Base, draft.Pending.Branch, draft.Pending.Commit, draft.Pending.File})

	stages, err := filepath.Glob(filepath.Join(o.ds.workspace, config.DefaultRuntimeDir, "agent-save-*"))
	require.NoError(t, err)
	require.Empty(t, stages)

	// A teammate's change to the file, pushed while the save runs, is reviewed, and nothing is pushed over it.
	o.ds.edit(t, id, &AgentDraftOp{Kind: AgentDraftEditBody, Variant: "a", Splices: []AgentDraftSplice{{Insert: "Again. "}}})

	theirs := "---\nmodel: gpt-6.1-terra\n---\nTriage.\n"

	moveBranch(func(dir string) { writeAgentFile(t, dir, "triage", theirs) })

	_, err = o.save(t, id, spec, false)
	review := refusal(t, err).Review
	require.NotNil(t, review)
	require.Equal(t, &saved, review.Base)
	require.Equal(t, &theirs, review.Live)
	require.Equal(t, theirs, testGit(t, o.remote, "show", "trunk:agents/triage.md"))
	require.Equal(t, AgentDraftEditing, o.ds.draft(t, id).SaveState)
	require.NoFileExists(t, marker)
}

// With gh logged in, a pull request opens from a branch of the save's own against the pinned branch, and
// the draft waits until gh reports it merged, then goes live; a pull request gh reports closed returns the
// draft to editing.
func TestSaveAgentDraftPullRequestWithGh(t *testing.T) {
	o := startAgentDraftOverlay(t, true, "@trunk")
	spec := teamOverlay + "@trunk"
	id := o.copyDraft(t)
	revision := o.ds.draft(t, id).Revision

	require.NoError(t, os.WriteFile(o.state, []byte("OPEN\n"), 0o600))

	state, err := o.save(t, id, spec, true)
	require.NoError(t, err)
	require.Equal(t, AgentDraftWaiting, state)

	saved := "---\nmodel: gpt-6.1-sol\n---\nKindly. Triage.\n"
	branch := fmt.Sprintf("rocketclaw-agent-draft-%s-%d", id, revision+1)
	pending := o.ds.draft(t, id).Pending
	require.Equal(t, [5]string{"trunk", branch, o.head(t, branch), "https://github.com/acme/team/pull/7", saved}, [5]string{pending.Base, pending.Branch, pending.Commit, pending.Link, pending.File})
	require.Equal(t, 7, pending.Number)
	require.Equal(t, saved, testGit(t, o.remote, "show", branch+":agents/triage.md"))
	require.Equal(t, triageFile, testGit(t, o.remote, "show", "trunk:agents/triage.md"))

	created, err := os.ReadFile(o.created)
	require.NoError(t, err)
	require.Contains(t, string(created), "pr create --base trunk --head "+branch+" ")

	require.Equal(t, AgentDraftWaiting, o.merged(t, id))

	o.teammate(t, func(dir string) { testGit(t, dir, "merge", "--squash", "origin/"+branch) })
	require.NoError(t, os.WriteFile(o.state, []byte("MERGED\n"), 0o600))
	require.Equal(t, AgentDraftLive, o.merged(t, id))
	require.Equal(t, &saved, o.ds.agentFile(t, config.DefaultRuntimeDir, "triage"))

	o.ds.edit(t, id, &AgentDraftOp{Kind: AgentDraftEditBody, Variant: "a", Splices: []AgentDraftSplice{{Insert: "Again. "}}})
	require.NoError(t, os.WriteFile(o.state, []byte("OPEN\n"), 0o600))

	state, err = o.save(t, id, spec, true)
	require.NoError(t, err)
	require.Equal(t, AgentDraftWaiting, state)

	require.NoError(t, os.WriteFile(o.state, []byte("CLOSED\n"), 0o600))
	require.Equal(t, AgentDraftEditing, o.merged(t, id))
	require.Equal(t, AgentDraftWaitClosed, o.ds.draft(t, id).Pending.Ended, "the page says the pull request was closed")
}

// Without gh, a pull request is a compare link, and an overlay pinned without a ref saves to the remote's
// default branch. The draft goes live once that branch holds the commit, as a merge commit does; after a
// squash merge it waits until someone stops waiting, which leaves the pull request open. A later pull
// request pushes a branch of its own, leaving the earlier one as it was.
func TestSaveAgentDraftPullRequestWithoutGh(t *testing.T) {
	o := startAgentDraftOverlay(t, false, "")
	id := o.copyDraft(t)

	layers, err := o.ds.rt.Sessions.AgentDraftLayers(t.Context(), id, "a", teamOverlay)
	require.NoError(t, err)
	require.Equal(t, [2]string{"trunk", ""}, [2]string{layers.Branch, layers.Unpushable})

	prSave := func() *AgentDraftPending {
		t.Helper()

		state, err := o.save(t, id, teamOverlay, true)
		require.NoError(t, err)
		require.Equal(t, AgentDraftWaiting, state)

		pending := o.ds.draft(t, id).Pending
		require.Equal(t, "https://github.com/acme/team/compare/trunk..."+pending.Branch+"?expand=1", pending.Link)
		require.Zero(t, pending.Number)
		require.Equal(t, AgentDraftWaiting, o.merged(t, id))

		return pending
	}
	edit := func(words string) {
		t.Helper()
		o.ds.edit(t, id, &AgentDraftOp{Kind: AgentDraftEditBody, Variant: "a", Splices: []AgentDraftSplice{{Insert: words}}})
	}

	first := prSave()

	o.teammate(t, func(dir string) { testGit(t, dir, "merge", "--no-ff", "-m", "merge", "origin/"+first.Branch) })
	require.Equal(t, AgentDraftLive, o.merged(t, id))
	require.Equal(t, &first.File, o.ds.agentFile(t, config.DefaultRuntimeDir, "triage"))
	o.requireShallowRuntimeClone(t)

	edit("Again. ")

	open := prSave()

	state, err := o.ds.rt.Sessions.StopAgentDraftWaiting(t.Context(), id, "bob", AgentDraftWaitStopped)
	require.NoError(t, err)
	require.Equal(t, AgentDraftEditing, state)

	open.Ended = AgentDraftWaitStopped
	require.Equal(t, open, o.ds.draft(t, id).Pending, "the page still shows the open pull request")

	openHead := o.head(t, open.Branch)

	edit("Third. ")

	squashed := prSave()
	require.NotEqual(t, open.Branch, squashed.Branch)
	require.Equal(t, openHead, o.head(t, open.Branch))

	o.teammate(t, func(dir string) { testGit(t, dir, "merge", "--squash", "origin/"+squashed.Branch) })
	require.Equal(t, AgentDraftWaiting, o.merged(t, id))

	state, err = o.ds.rt.Sessions.StopAgentDraftWaiting(t.Context(), id, "bob", AgentDraftWaitStopped)
	require.NoError(t, err)
	require.Equal(t, AgentDraftEditing, state)

	state, err = o.ds.rt.Sessions.StopAgentDraftWaiting(t.Context(), id, "bob", AgentDraftWaitStopped)
	require.NoError(t, err)
	require.Equal(t, AgentDraftEditing, state, "only a waiting draft stops waiting")
}

// A merge check ends only the wait it read: one that finishes after a newer save began waiting leaves that
// wait alone.
func TestAgentDraftMergeCheckEndsOnlyItsOwnWait(t *testing.T) {
	o := startAgentDraftOverlay(t, false, "@trunk")
	id := o.copyDraft(t)

	state, err := o.save(t, id, teamOverlay+"@trunk", true)
	require.NoError(t, err)
	require.Equal(t, AgentDraftWaiting, state)

	first := o.ds.draft(t, id).Pending
	o.teammate(t, func(dir string) { testGit(t, dir, "merge", "--no-ff", "-m", "merge", "origin/"+first.Branch) })

	// While the check reloads, the wait it read ends and a newer save starts waiting.
	newer := *first
	o.ds.validate = func(string) error {
		o.ds.validate = func(string) error { return nil }

		_, err := o.ds.rt.Sessions.StopAgentDraftWaiting(t.Context(), id, "bob", AgentDraftWaitStopped)
		require.NoError(t, err)

		newer.Branch, newer.Revision = "newer", o.ds.draft(t, id).Revision+1
		_, _, err = o.ds.rt.Sessions.moveAgentDraft(t.Context(), id, "alice", AgentDraftWaiting, nil, agentDraftWait{pending: &newer})
		require.NoError(t, err)

		return nil
	}

	_, err = o.ds.rt.Sessions.CheckAgentDraftMerge(t.Context(), o.ds.rt, id, "bob")
	require.NoError(t, err)

	draft := o.ds.draft(t, id)
	require.Equal(t, AgentDraftWaiting, draft.SaveState)
	require.Equal(t, &newer, draft.Pending)
}

// A draft whose file the overlay's branch already holds has nothing to commit: a pull request is refused,
// and a push commits nothing and goes live on the commit the branch already has.
func TestSaveAgentDraftOverlayAlreadyHasFile(t *testing.T) {
	o := startAgentDraftOverlay(t, false, "@trunk")
	spec := teamOverlay + "@trunk"
	heads := testGit(t, o.remote, "ls-remote", "--heads", o.remote)

	draft, err := o.ds.rt.Sessions.CreateAgentDraft(t.Context(), "alice", AgentDraftOrigin{Name: "triage", Copy: true})
	require.NoError(t, err)

	_, err = o.save(t, draft.ID, spec, true)
	require.Equal(t, "Branch trunk of github.com/acme/team@trunk already has this file, so a pull request would have nothing to commit.", refusal(t, err).Problem)
	require.Equal(t, AgentDraftEditing, o.ds.draft(t, draft.ID).SaveState)

	state, err := o.save(t, draft.ID, spec, false)
	require.NoError(t, err)
	require.Equal(t, AgentDraftLive, state)
	require.Equal(t, heads, testGit(t, o.remote, "ls-remote", "--heads", o.remote))
	require.Equal(t, o.head(t, "trunk"), o.ds.draft(t, draft.ID).Pending.Commit)
}

// An overlay pinned to a tag cannot be pushed to, and a draft that does not load, whose stored name is not
// one, or whose file Git would commit changed, as an ident attribute does, is refused, all before anything
// reaches the remote.
func TestSaveAgentDraftOverlayRefusals(t *testing.T) {
	o := startAgentDraftOverlay(t, false, "@trunk", "@v1")
	id := o.copyDraft(t)

	o.teammate(t, func(dir string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.md ident\n"), 0o600))
	})

	heads := testGit(t, o.remote, "ls-remote", "--heads", o.remote)

	o.ds.edit(t, id, &AgentDraftOp{Kind: AgentDraftEditBody, Variant: "a", Splices: []AgentDraftSplice{{Insert: "$Id: mine $ "}}})

	_, err := o.save(t, id, teamOverlay+"@trunk", false)
	require.Equal(t, "Git would commit other bytes than the checked file, so nothing was pushed.", refusal(t, err).Problem)

	pinned := "github.com/acme/team@v1 is pinned to a tag or commit, so a save cannot push to it; pin it to a branch to save there."

	layers, err := o.ds.rt.Sessions.AgentDraftLayers(t.Context(), id, "a", teamOverlay+"@v1")
	require.NoError(t, err)
	require.Equal(t, [2]string{"", pinned}, [2]string{layers.Branch, layers.Unpushable})

	_, err = o.save(t, id, teamOverlay+"@v1", false)
	require.Equal(t, pinned, refusal(t, err).Problem)

	o.ds.edit(t, id, &AgentDraftOp{Kind: AgentDraftSetField, Variant: "a", Key: "sticky", Value: "sometimes"})

	_, err = o.save(t, id, teamOverlay+"@trunk", false)
	require.NotEmpty(t, refusal(t, err).Problems)

	_, err = o.ds.rt.Sessions.db.ExecContext(t.Context(), `UPDATE agent_drafts SET draft = jsonb_set(draft, '{name}', '"--upload-pack=x"') WHERE id = $1`, id)
	require.NoError(t, err)

	_, err = o.save(t, id, teamOverlay+"@trunk", true)
	_, ok := errors.AsType[*AgentDraftNameError](err)
	require.True(t, ok, "%v", err)

	require.Equal(t, heads, testGit(t, o.remote, "ls-remote", "--heads", o.remote))
	require.Equal(t, AgentDraftEditing, o.ds.draft(t, id).SaveState)
}
