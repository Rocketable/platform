# RocketClaw Web

Every tab's menu, Shared rows included, has **Copy link** (also **Tabs: Copy link**
in the command palette). On the tab in view it copies the full current address,
including query and fragment; on another tab, that tab's address. Plain HTTP uses
the same clipboard fallback as message copying.

Assistant replies show a smaller, muted footer with known
`agent (provider/model#effort)` details; `#effort` is omitted when absent.
On desktop, hover a message to see its footer and Copy button. On touch
screens, tap a message instead. User messages show Copy and, when recorded, a
message-header info button, but no agent or model label.
Human messages also show their recorded author as a small, always-visible label
above the text inside the bubble. Queued/stashed rows and pending steers retain
their own author across viewers, reloads, and promotion. Rows another session's
agent wrote with `rocketclaw_sessions_write` show `agent <name> (session <ID>)`
as their author. Removing such a row tells the writing session, with a note,
that its message was removed before it ran. The info control keeps
the exact raw prompt header. Historical messages without author metadata stay
unlabeled; text that looks like a header is not treated as attribution.
New composer and handoff previews snapshot the server-resolved author only when
identity is ready, not refreshing. Recorded history or queue data replaces that
preview by input ID; the current viewer never supplies an author for saved messages.
Copy puts the message text on the clipboard.
**Raw** message text follows Slack formatting: `*bold*` and `**bold**`, `_italic_`,
`~strikethrough~`, `` `inline code` ``, fenced code blocks, `> ` quotes, and
`- `/`* `/`1. ` lists (nested items render flat). Markdown, Slack, and bare
`http(s)` links are clickable and open in a new tab. Slack tags show names:
`<@U…>` as `@display name`, `<!subteam^S…>` as `@handle`, `<#C…>` as `#channel`,
and `<!here>` as `@here`. Names come from Slack and refresh every 8 hours; a tag
that cannot be resolved shows its ID, such as `@U123`. Headings and tables stay
literal. Session titles, turn previews, handoff results, and queued items show the
same inline formatting on one line; their tooltips and accessible names use the
plain text with names filled in. Copy still copies the original text.
Chat messages default to **Markdown** output instead: GitHub-flavored Markdown with headings, tables,
nested lists, task lists, and rules, with single line breaks kept. Code blocks,
links, and Slack tags look as in Raw; raw HTML stays literal. The command
palette's **Output: Raw** and **Output: Markdown** switch between them, and this
browser remembers the choice across reloads and tabs. Search results, previews,
and voice transcripts always use Raw.

GitHub pull-request links have an explicit **Preview** action beside the ordinary
external link, in Raw and Markdown. Preview starts as a modal sheet on the right; its
layout selector also offers Dialog, Inline and Full-screen without fetching again.
Its compact header and **Summary / Code** tabs follow t3code's PR detail layout.
The header shows lifecycle status, author/update time and branch/change counts
when GitHub supplies them. **Code** uses Pierre CodeView and FileTree, bundled
locally: a scrolling diff, file navigation, and Unified/Split layouts. No iframe,
hosted DiffsHub request, external renderer service, or renderer credential is used.
Files still load on demand. Counts show partial results;
GitHub exposes at most 3,000 files, and patches can be omitted or incomplete.
Revision checks before and after each file page reject observed base/head/count
changes. They are not an atomic snapshot: a branch changing A→B→A between checks
can evade detection. Refresh or open GitHub when the branch is changing.

Public previews use anonymous browser GETs directly to `api.github.com` and have
a limited GitHub request quota. Your own fine-grained token with **Pull requests:
read** can increase that quota or provide private access. Token entry is available
after read failures, including file-page failures, and requires a secure browser
context and a trusted certificate. The token and result stay only in that open
preview's memory: closing, navigating away, hiding the conversation tab or
unmounting clears them and cancels requests. Switching browser tabs keeps the
preview open. Reopening a closed preview is anonymous. **Cancel** while files
are loading stops that request and keeps loaded content; **Close preview**
discards the preview.
Tokens are not saved, shared with the application server, or sent to telemetry.
Preview DOM and GitHub network data are excluded from PostHog recording.
Browser extensions and clipboard managers remain outside the
application's control. No remote images or executable description content load.

The conversation tab's **⋯ → Pull requests** menu item opens the existing right
pane for a RocketClaw or OpenCode thread. So does the bottom bar's right-end
button, which closes whichever right pane is open, and Cmd/Ctrl+Shift+B, which does
what that button does. The key is RocketClaw's even while the button is disabled,
so Chrome's bookmarks bar toggle never takes it. On desktop the conversation
stays editable beside it; on narrow screens it uses a sheet. It replaces an open
delegation or skill pane. It shows a PR's description and changed-file patches;
when the thread has more than one PR, a picker in the pane's header selects it.

On desktop the pane also opens by itself, on that PR, when the thread in view
gains a PR or one of its PRs gets a new head commit. Discovery then runs after
every update of that thread, open pane or not. Head commits are read through an
OpenCode server's `gh` login only, so for a native RocketClaw thread only a new
PR opens the pane. The first look at a thread, a PR missing from one look, and a
head that could not be read never open it.

A PR counts as created in this thread when a successful recorded shell call runs
`gh pr create`, alone or in a script: each GitHub PR URL it prints alone on a line
counts, as `gh pr create` prints them. A script that also prints some other PR URL
alone on a line claims it too. Prompts, ordinary links, quoted mentions,
permission-review plans, dry runs, and failed calls do not establish ownership.
OpenCode discovery also reads the thread's subagent sessions, and theirs, since
subagents often open the PRs. Native RocketClaw uses the existing bounded Code Mode
call summaries; clipped summaries and older history without actual-call records can
miss a PR. Discovery reads older history pages too. It runs when the pane opens,
refreshes after thread updates, and can be repeated with the header's **Refresh**
button, which also reads the PR shown again.

OpenCode also checks the session folder using its server's installed `gh` and
existing GitHub login. In a jj workspace, whose Git checkout has no current branch,
it asks `gh` about the nearest jj bookmark at or below the working copy.
**Current checkout** names the branch result, not proof
that the PR belongs to this thread: several sessions may share that folder. A PR
that finished before the session existed is not suggested from the checkout.
OpenCode PR details and patches use that same server login, including private
repositories; anyone allowed to use that OpenCode server through RocketClaw can
therefore read those PRs. No GitHub token enters the browser. Lookups use standalone
shell commands, not prompts or conversation turns. While the browser continues
running and DELETE succeeds, it removes lookup shell records after use, including
caller cancellation. Browser loss or a failed DELETE can leave a record behind.
Discovery traffic, ordinary History responses, and pane content are
excluded from telemetry. There is no automatic settlement, archive, deletion, or merge.

An executable [PlusCal model](../../../docs/verification/thread-pr/README.md)
checks bounded discovery, reader and shell-lifecycle cases with TLC, including
negative cases that must produce counterexamples. Its source mapping, bounds,
fairness assumptions, ABA counterexample and cleanup limits are documented there.
This is model verification, not a proof of the TypeScript, renderer, or network.

Code blocks and tool output are shadcn/ui
[Code Blocks](https://ui.shadcn.com/docs/components/base/code-block) and scroll
sideways by default. A fenced block whose language is bash/sh, CSS, diff, Go, HTML,
JavaScript/JSX, JSON, Markdown, Python, SQL, TypeScript/TSX, or YAML is syntax
highlighted once that language loads; tool output and search results are not. Their wrap button, also in
the expanded view, wraps long lines for every block at once; this browser
remembers the choice across reloads.
Only sessions with sandboxed messages append ` - origin` to the footer. The
origin is `sandboxed` for messages produced by another conversation or a cron
run, and `canonical` otherwise. New turns store the
resolved provider-qualified model.
The footer does not show source or destination conversation IDs. Copied messages
retain the producer's settings.
These are execution-time snapshots, so changing the selected agent does not
relabel saved messages. Missing historical values are omitted, and old bare
model names are shown as recorded without an assumed provider. Optimistic
inputs are enriched by message ID when their consuming runtime is ready.
Tool progress shows its state and producer settings, without a Copy action.
Completed automatic permission reviews show their recorded rationale as one line
of small text under the tool response, including reviews of nested host-tool
calls; its icon opens the review. Pending reviews and histories without a
recorded completed final response have no footer.
Reasoning groups have no footer or Copy action.
When a session contains sandboxed messages, a slim button group above the
composer highlights both `sandboxed` and `canonical` by default. Each button
independently toggles its origin on or off, so both, either, or neither can be shown.
Messages without a recorded origin appear only when both are selected.
Filtering does not change saved history or routing.

## Shadow comparison

Agents can declare [shadow definitions](../../../README.md#shadow-agents).
Open their parent session to inspect the shadows in the right sidebar, with one
tab per shadow and agent/model attribution. The comparison is read-only: send,
steer, stop, and edit through the parent, not the shadow.

On wide screens, parent and selected-shadow turns share one scroll area. Each
paired row is as tall as its longer side, so the next pair begins at the same
height. Steers stay inside their original turn, at the point they were used.
Repeated prompt text and different completion speeds do not change pairing.
Pending, silent, failed, and stopped turns keep their rows. Turns before a
shadow was attached, or belonging to another parent agent, show an unavailable
comparison rather than shifting later turns.

Changing tabs reads the selected shadow's history; it does not open another live
stream. Opening a skill or delegation replaces comparison until that panel is
closed. On narrow screens, **Shadows** above the composer opens a full-screen
comparison panel with turn navigation. It shares a row with the source filters
when present, but does not change them. Tabs identify the shadow; row headings
show only the turn number. Side-by-side alignment applies when both columns are visible.

**Read-only describes the Web controls, not tool execution.** A shadow can still
edit files or repeat external effects under its own permissions. Its replies and
attachments stay in comparison and do not create extra connector deliveries.

## Agent editor

`/agents/new` creates an agent, and `/agents/drafts/<id>` opens one of the
drafts the server stores (see "Agent Draft" in `CONCEPTS.md`). The page's address
is the draft's link: **Share** copies it, and any teammate without space limits
who opens it edits the same draft. People limited to spaces get a plain refusal
and a link back to the read-only Agents page instead, because a draft can give
itself any permission and its test chats run real tools; the server refuses them
every editor call too.

The Agents page has **Create New Agent** and, on each agent, **Edit**, which
reopens the newest draft of that agent still in editing, or else starts one as a
copy. People limited to spaces do not see either button.

The page is a workbench: settings on the left, the test chat in the middle, and
the assistant on the right, under a header with the draft's save state, **Save…**,
**Easy** and **Advanced**, **Drafts**, the people who have the draft open, and
**Share**. Easy or advanced is the whole draft's: switching it converts every
variant at once, for everyone who has the draft open. **Drafts** lists every draft
on the server with its save state, mode and variant count, and **Start a new
agent**.

Before there is a draft, the settings pane asks "Easy agent or advanced agent?"
first, then how to start: **Describe it** (name the agent, then describe it to the
assistant), **Template** (a starter agent, which brings its own settings,
instructions and mode), **Copy an agent** (a new agent, named after the original
with `-copy` unless you name it, leaving the original as it is), or **Blank**. The
copy list also offers **Edit** on each agent.

**Form, File and History.** The settings pane edits one variant at a time. The
Form shows required, common and changed settings with a plain explanation each,
and folds the rest into groups that say their defaults in words. The File tab is
the same variant as its agent file. Everyone's edits in either tab, and their
cursors, appear for everyone else as they type. A setting takes the last write;
instructions typed at the same time keep everyone's text. File-tab YAML that does
not parse yet stays with the person typing it until it does. History lists every
change by person and setting, and **Restore** brings an earlier version back as a
new change.

**Model picker.** A model setting offers only the providers this RocketClaw has
configured. For a provider that models.dev knows by the same name, the browser
fetches models.dev when the picker is first used and suggests models with their
price, context size and effort levels, each linking to slopalytics to compare. Any
model name can be typed. **Test** sends one small paid request to that model, only
when clicked, and says "Works" or shows the provider's error.

**Test chat.** One message goes to every variant, and to the live agent when
editing an existing one, and the answers line up side by side under it. Each
answer names the version that gave it and fades once its column changes. The page
warns that test messages run real tools once per column. **Restart** starts an
empty chat; **Restart & replay** sends the earlier messages again. **Test as
External MCP** makes every column answer as an External MCP call, with a caller
name and metadata pairs; it is only for `sticky: off` agents. Keys sent with the
first message stay fixed for the chat, so changing one offers **Restart with new
metadata**, and a key added later goes with that message only. When the
instructions read `$ROCKETCLAW_METADATA_*`, the page suggests this mode first.

**Assistant.** Describe what to change, or press **Fix this** on a test answer.
The assistant answers with a card of suggested changes, each with a reason, and
marks the ones that change what the agent may do. The Form and the File preview an
open card. Anyone on the draft can accept or reject it; the assistant never saves
and never runs tools.

**Saving.** **Save…** saves one variant to one place: the workspace's `agents/`
folder or a Git overlay from `rocketclaw.json`. The dialog lists the layers with
the one that wins on top, says which provides the agent now, and warns when a
later layer hides the destination or when the save replaces a built-in or overlay
agent whose updates then stop reaching this workspace. It shows the file the save
writes against the one there now: settings go in a standard order and comments
are dropped. For an overlay it asks every time whether to push to its branch or
open a pull request. If the destination file changed since the draft began, a
review lists each changed setting with **Keep mine** or **Take theirs**, flags the
ones both sides changed, and offers the file's instruction changes; then it saves
again. A pull request leaves the draft "Waiting for merge", with a link to it and
**Stop waiting**, until it merges and the agent goes live. Stop waiting, or the
next edit, returns the draft to editing, and the page notes that the pull request
is still open on GitHub; a pull request `gh` reports closed returns it too, with a
note that it was closed. Settings that do not load stop the save, and the Form
says why under each one.

An open page follows the draft's stream, `/stream?id=draft:<id>`, which only wakes
it: the page then reads what changed since its revision. It also tells the server
every 10 seconds that it is open, since the server lists only editors seen in the
last 30 seconds.

## Revert, Undo, and Redo

Recorded user messages in Web chats, Slack threads, and shadows have a **Revert
message** action alongside Copy. Hover on desktop, tap on touch screens, or focus
the message with the keyboard. Revert reaches only the messages after the newest
one from a cron run, External MCP, or Slack: that message and everything before
it stay, and their Revert is disabled. Hidden cron and External MCP runs and
delegation histories do not support Revert. Pending inputs keep their withdrawal
controls.

Revert stops and settles active work, hides the selected request and everything
after it, and replaces that session's composer text and files with the request.
It sends nothing and keeps the current agent and submitting identity.
`$undo` (also in the command palette) moves to the previous recorded request;
repeated Undo can leave an empty conversation. **Redo** or `$redo` restores the
whole hidden suffix without changing the composer. While history is reverted, a
[Checkpoint](https://ui.shadcn.com/docs/ai-elements/base/checkpoint) divider below
the last kept turn says so and offers **Redo**.

The cutoff survives reload and daemon restart. Waiting human work stays stored
but hidden until Redo, which restores its order. Sending a new prompt—including
Queue or Stash—commits the new branch in the same session and drops the abandoned
history and waiting human inputs. Redo is then unavailable. Preparation or
admission failure keeps the cutoff and edited draft recoverable.

Composer content is saved locally in IndexedDB, including supported file bytes,
scoped to this browser origin, authenticated username, configured workspace and
session. It is not shared with other viewers; their history updates but their
drafts do not. A storage failure shows an error: keep the page open because
reload persistence is unavailable. Rejected Identity or config reads disable
draft hydration and editing until both are accepted. **New session** clears the
Home draft from local storage before opening a fresh composer; storage failures
are reported without discarding its content. Clearing browser site data deletes drafts.

**Revert changes conversation history only.** It does not undo files, tool
effects, delivered messages or scheduled registrations. Scheduled execution
pauses while reverted and may resume when the cutoff clears.

The frontend is a client-side React SPA. Bun builds static assets directly into
`../internal/web/dist/`: `index.html`, hashed JavaScript and
CSS, and a self-hosted Inter font. `cmd/rocketclaw` embeds and serves that directory
alongside the Go HTTP API. Production needs no Bun, Node, or Next server.

See [Web conversation transport](../frontend/rpc/README.md)
for startup commands, browser identity and the transport contract. The Go
handler takes identity from the browser connection; forwarded headers cannot
select a user. Serve it directly to browsers rather than through another HTTP
reverse proxy. Phones reach it over HTTPS through RocketClaw's own Tailscale node;
see [Voice](#voice).

## Voice

The **Voice** page (`/voice`, also in the bottom navigation and the command
palette) calls the signed-in person's own session of a `sticky: user` agent, the only
agents it offers. RocketClaw keeps the person's agent, voice and transcript choice on the
server: the same on every device and through restarts, and never shared with another
person, even in one browser. Pick an agent and tap the orb to talk; tap it again to hang up. While a call is
active the orb slowly ebbs and flows, its inner light swirls around a glowing heart,
and two orbit rings spin around it; it swells and brightens with the voice model's
speech, measured in the browser from the incoming audio. With reduced motion it stays still. The first
tap creates the agent's sticky session and later taps, on any device, reuse it; each tap
asks the server for it first. Picking another agent while idle saves it and moves to that
agent's own sticky session; no conversation switches agent. A voice picker next to the agent picker lists the voices the
default `openai` provider offers (ChatGPT-login and API-key providers offer different ones).
Each person has one voice: a call uses it when the default `openai` provider offers it, and
that provider's default otherwise, and the composer's voice button follows it too. Icon buttons on the right show the transcript and
open the conversation, once the agent has one. The row stays on one line: on phones the agent picker drops
its robot icon, and a long agent name is shortened. The page shows the call state, elapsed time and notices
under the orb. **Show transcript** swaps the big orb for live captions (not saved)
shown as a chat, with a small orb at the bottom that still starts and ends the call;
the choice is kept with the person's voice settings. Leaving the page hangs up, and cancels a start
still in progress; a session it already created stays.
The screen stays awake while the Voice page is open or a call runs.

Each session's composer has a voice button, on every screen size, that starts and
ends a call on that session. It is not dictation: nothing is typed into the composer.
On New session it first creates the session with the shown agent, as the first send
does, then calls on it. Leaving the session ends its call.

New session opens a sticky agent's existing session instead of starting one (see the
[root README](../../../README.md#sticky-agents)), with an alert saying so: at once when
the agent is picked and the catalog knows its session, else when the first message, or
the voice button's call, goes there. A sticky session's picker offers only its agent,
and other sessions' pickers offer no sticky agent.

A tab runs one call at a time, and each person has one live call: starting voice
anywhere else, on any conversation, ends their older call with "Voice moved to another
device or tab." Other people's calls, even on the same conversation, keep running.
A call also ends when the page closes or stays hidden for 30 seconds. There are no
idle or no-speech timers. A hand-off the agent could not accept shows a notice, and
the call continues.

Every call keeps going through dead zones. A call that loses its connection (the
audio connection fails, the page loses RocketClaw, RocketClaw loses OpenAI, or the
audio is not up 20 seconds after OpenAI answers) starts a new call on the same
conversation and microphone instead of ending. The first try comes 1 second
later, and each failed try doubles the wait, up to 10 seconds, until it comes
back or is hung up; the status reads "Reconnecting…" meanwhile. A falling two-note
tone marks the loss, a short tick plays every 5 seconds while reconnecting, and a
rising two-note tone marks the call coming back. Each new call is a fresh OpenAI
call that starts from the conversation's recent messages, so talk never handed off
to the agent is not carried over. Other failures, such as another device taking
the call, OpenAI ending it, or a refused credential, end the call with a message
and are not retried.

The agent's final reply to a hand-off is spoken once, as written. A reply over
about 990 tokens is not spoken: the voice says it is on the screen, and the Voice
page turns its transcript on and shows the reply there, formatted as in the
conversation. Questions are announced and
answered in the app, never by voice. Voice needs microphone permission and a
secure page: HTTPS, or `http://127.0.0.1` on the RocketClaw machine. See the
[root README](../../../README.md#voice) for phone setup with `web.tsnet`,
credentials, cost, and what OpenAI receives.

## Local checks

Use Bun 1.4.4 or newer. From this directory, run:

```sh
bun install --frozen-lockfile
bun run lint
bunx tsc --noEmit
bun run build
make test
```

Set `ROCKETCLAW_TEST_DATABASE_URL`, `ROCKETCLAW_PLAYWRIGHT_MODULE`, and
`ROCKETCLAW_CHROMIUM` for the full test run. `make test` builds the SPA, runs the
standalone Bun test files in up to four isolated processes, then runs
transport/browser tests through their Go-owned PostgreSQL and provider fixtures.
Running those transport files directly with
`bun test` does not create their fixtures.

Build these assets before building `cmd/rocketclaw`, then run that Go executable;
it serves the Web on its `web.tsnet` node's HTTPS name. Navigation uses the browser History API;
non-API page paths must return `index.html`, including direct `/s/<base64url-id>`
and `/cron` loads. The source lives in `internal/rocketclaw/web/`; no JavaScript server is shipped.
Keep `TMPDIR` inside the repository's `.tmp/` for builds and tests.

The local `go.mod` is a boundary that keeps this JavaScript project and its
`node_modules` dependencies out of repository Go test and lint scans. CLOC also
excludes `node_modules`; frontend source uses the separate JS/TS budget.

The Config page shows the configured Web username and the connected device's
Tailscale user login. Browsers are admitted by their Tailscale login, which the
`web.tsnet` node looks up for each connection; it refuses tagged or unknown devices.
New messages use `UserProfile.DisplayName`, falling back to the admitted login
when the display name is missing or blank. Ownership, routing, and Config's
Tailscale display remain login-based.

Users named in the config file's `web.admins`, unless limited to spaces, also
get a **Behavior settings** section on the Config page. **Edit** shows one control per Behavior Setting
(see the [root README](../../../README.md#getting-started) for which fields they
are); **Save** replaces the whole document and applies it to the running server,
or shows the validation error and changes nothing. Everyone else sees the page
read-only, without the settings document or Slack allowed user IDs.

Oxlint runs `@shadcn/lint` with `no-restyle` enabled, including inside the UI
components. Use component variants for styling and `className` for layout;
the configuration is in `.oxlintrc.json`.

The UI uses the shadcn `b0` preset with Base Nova components, configured in
`components.json`. Message rows compose `Message` and `Bubble`; `MessageScroller`
owns streaming follow and turn jumps. The turn rail uses its `scrollToMessage`
API, and sending a message resumes following through `scrollToEnd`.

The build includes TypeScript checks. The entry, sidebar and public-progress HTTP integration
tests fail unless their Go test servers are running. `go test` in
`frontend/rpc` runs each of them against its own server, which is how the
`rocketclaw` job in `.github/workflows/test.yml` runs them. Use the isolated
PostgreSQL and browser-test instructions in the transport README to exercise
those paths. Keep temporary test artifacts under the repository's `.tmp/`.

## Remote RocketClaws

The page lists the remotes the local config names (see the
[top-level README](../../../README.md#remote-rocketclaws)) from a `rocketclaws`
script tag in `index.html`. A remote's views live under `/r/<name>/`, followed by
that server's own path, so `/r/work/s/<id>` is chat `<id>` on `work`.
`navigation.tsx` owns the rule: `usePathname` returns the path without the
prefix, and `navigate` and `Link` keep the page's server unless told another.
Under a remote, every RPC, the session stream, uploads, downloads, Voice, hosted
page frames and the OpenCode view use that remote's origin, and the remote
signs the browser in itself. Query keys belong to the page's server unless they
start with `{ server }`.

The session list polls each server on its own schedule and merges their rows by
recency; a row is its chat on its server. `viewing` goes only to the page's
server. A remote that stops answering keeps its rows, greyed and offline; only a
refusal clears them. Search fans out to every server, and a failing remote drops
out. Each server marks its own tabs (see Unread below), and the page notifies
from every server's listing; a server that is offline or on another protocol
offers no attention items in its rows' menus. The tab strip lists every
server's cards, one for each of its spaces, in one list (see Spaces below). Another
server's cards show its saved tabs, and changes to them save on that server;
entering one of its spaces or opening one of its tabs opens that server's view
in that space.

The local server's protocol change reloads the page. A remote that never
answered shows why it is unavailable, one that stopped answering shows it is
offline, and one on another protocol keeps working with a notice.

## Tabs

Tabs follow editor semantics.
Sessions and pages (New session, Search, Voice, Cron, Agents, Skills, Settings) open in
one strip, and a location never appears in two tabs. The URL always names the
active tab.

- **Opening.** Opening a location that is not already open (New session,
  Search results, links, footer buttons, page commands, the palette) gives it a
  tab of its own, right of the active tab. Arrowing through the palette's
  sessions shows each in one preview tab, drawn in italics and never saved,
  without adding history entries; a session that would open in another space is
  skipped. Choosing the session keeps it as an ordinary tab; anything else closes
  the preview tab. The palette's commands act on the
  location it opened at: typing or pressing a command returns there first, and
  dismissing the palette returns there too. Sending the first message from New
  session turns that tab into the new session's tab.
- **Context menu.** Right-click a tab, press Shift+F10 or the menu key on a focused
  tab, or use the **…** button on the active tab (for touch) to reach Close,
  Close others, Close to the right, and Close all. Session tabs also offer **Rename session**, which edits the name in
  the tab itself: Enter or clicking away saves and Escape cancels. A blank name
  generates a new title from the conversation, as **Regenerate title** does.
  Session tabs also offer **Delete session**, as
  does the command palette for the current session; it asks first and then
  closes the tab. A remote session is deleted on its OpenCode server; a RocketClaw
  session is deleted for everyone, as retention cleanup deletes it, unless it is
  running or holds queued work, which must be stopped first. As in a macOS menu, a line separates its
  sections: renaming, closing, sharing, attention, grouping and moving, then
  deleting. The command palette offers the same commands as **Tabs: …** for the
  active tab.
- **Selecting tabs.** Cmd/Ctrl+click adds a tab to the selection or removes it,
  and Shift+click selects the shown tabs from the active tab to the clicked one;
  the active tab stays active and always belongs to the selection. The menu of a
  selected tab acts on every selected tab for Close, the group commands, and the
  moves to another space, and
  it drops Rename session and Restore; any other tab's menu acts on that tab alone.
  Activating another tab, a plain click on the active one, or Escape on a focused
  tab drops the selection. Selected tabs are highlighted, and screen readers hear
  “In selection” in their description.
- **Full names.** A title longer than the sidebar truncates. Hovering a tab
  shows its full title in a tooltip; while it shows, the first
  Escape dismisses it.
- **New session.** Double-clicking empty space in the strip opens New session,
  like the New session button.
- **Closing.** The × button, a middle-click, the menu, a page's close button, or
  Escape on a page closes a tab. Closing the active tab activates its right
  neighbour, or its left one at the end. Closing the last tab opens New session;
  New session alone does not close. Closing with the × button or the menu moves
  keyboard focus to the selected tab. A page that is only in Shared has no close
  button, and Escape leaves it open.
- **Recently closed.** The server keeps your last 50 closed tabs, newest first,
  with the space each was in and a Search page's searches; New session is never
  one. **Tabs: Recently closed** in the command palette opens the `/closed` page,
  which lists them with their spaces. Choosing one reopens it in its space, or in
  the current space when that space is gone, and Cmd/Ctrl+Alt+T (or **Tabs:
  Reopen closed tab**) reopens the newest, in a remote OpenCode session too, whose
  New terminal command has no default key for that reason. A reopened tab leaves the list, and so
  does a deleted session's tab. A preview tab is never saved, so it never lists.
- **Next tab needing you.** Cmd/Ctrl+Shift+A, the inbox button in the bottom
  navigation, or **Sessions: Next needing you** opens the next bold tab, of any kind (see Unread below): first
  sessions asking a question, then the rest, each longest waiting first.
  Pressing it again moves on to the next one and wraps around. Only tabs you
  see as a tab count: your own tabs in any space and Shared rows, on any
  server. Parked tabs, a session of a server that stopped answering, and a
  marked tab you no longer have do not. A Shared row opens in its space
  without a tab of its own. The window title counts the
  same tabs, as "(2) …". The command shows, and the button is enabled, only
  while another tab waits. The key is RocketClaw's even then, in a remote
  OpenCode session too, whose auto-accept permissions command has no default
  key for that reason; so is Cmd/Ctrl+Shift+S (see Park below).
- **Remote OpenCode sessions.** A remote session's tab shows OpenCode's own
  session view, copied into RocketClaw at `internal/rocketclaw/oc/` and mounted in
  the page with no frame. It reaches its server through the Web's
  `/oc/<server>/api/` route and follows RocketClaw's light or dark mode. Any session
  of a configured server opens, listed yet or not. A server that is offline before
  the view first opens shows a notice; once open, the view shows its own
  reconnecting banner. Forks and other referenced sessions open in new tabs, child
  sessions open inside the view, and deleting the shown session closes its tab.
  The view shows the session its tab names or one of that session's child
  sessions, even when tabs change while a session loads.
  Escape never closes a remote session's tab. RocketClaw's global shortcuts win
  throughout the view, including while OpenCode's terminal has focus, without
  also sending the shortcut to the terminal. ⌘P and ⌘⇧P open RocketClaw's palette.
  Other keys stay with the view; ⌘K opens OpenCode's palette from its composer.
- **Notifications.** The server decides when a tab becomes bold for you and
  whether that raises a notification (see Unread below); the browser only
  shows it. Each open window shows a desktop notification for a new mark that
  is not quiet, unless the tab is parked or is the tab in view (the active tab
  while the window has focus). It is titled with the tab's title: a RocketClaw
  session's name, or its agent when it has none, an OpenCode session's tab
  title, or any other tab's title. Its body says what happened: “Park ended”
  for a park's time, the start of the reply for any other session mark,
  “Search finished”, “Run finished” for a cron, or “<agent> · <page> updated”. Only a tab you have
  notifies: in your own tabs or Shared. News on a tab you
  closed shows quietly in bold when you open it again, and a window still
  holding tabs older than the server's waits for its reload before it notifies.
  Clicking the notification opens the tab where you have it: a Shared row
  opens in its space without a tab of your own. Notifications are on by default: the first click or
  key press asks for the browser's permission, the earliest a browser allows;
  turn them off in the browser's site settings. A new mark is noticed on the
  session list's poll, so a hidden window may notify late; a page load never
  notifies for marks that were already there. Notifications show only while a
  RocketClaw window is open; there is no push.
- **Mark as read and Mark as unread.** Every tab's menu, Shared rows
  included, offers **Mark as read** while a selected tab is bold and
  **Mark as unread** while one is read; with several tabs selected, each acts
  on the tabs it fits. The command palette offers the same as **Tabs: Mark as
  read** and **Tabs: Mark as unread** for the tab in view. Mark as read clears
  bold even while the agent waits on your answer; its **Needs you** badge
  stays. Mark as unread turns the tab bold without a notification. Marking the
  tab in view keeps you there and keeps the mark while you read it, through
  reloads too; it clears the next time you go to another tab, a palette preview
  included, and come back.
- **Park.** The menu's **Park** submenu offers, each set apart by a line,
  **Until it is done** on session tabs; 1 hour, 4 hours, Tomorrow 9:00, Next
  Monday 9:00, and **Pick a time…**; and **Until I unpark**. Pick a time… is a
  dialog that starts at the next whole hour, today unless that is past
  midnight, and refuses a time already past; times are the browser's local
  time. Until it is done lasts until the session's next turn
  ends, finished, failed, or stopped, also when nothing runs yet; Cmd/Ctrl+Shift+S
  parks the session in view that way, and on any other tab does nothing. A parked tab
  leaves the strip for the **Parked** section
  at the bottom, which lists your parked tabs from every space, each with when
  its park ends, and its header counts them. It folds like a card, starts folded, and folded still shows
  the tab in view. With more than one space it is a tree: each space with
  parked tabs is a branch that folds on its own, again still showing the tab in
  view, and stays as you left it. With Remote RocketClaws, when more than one
  server has parked tabs, each server is a branch above its spaces; a row opens
  on its own server in its space, its menu's attention items act there, and it
  has no close button. A parked tab keeps its space, group, and place, and
  **Unpark**, in its menu in place of Park, puts it back there.
  It is never bold and never notifies, but what happens on it is remembered:
  unparked, it is bold if something happened that you have not seen, without a
  notification. It comes back by itself, bold with a notification, when its
  time is up (“Park ended”), when the turn it waited for ends (the reply's own
  notification), or when its agent asks you a question, within a poll, also
  when you had closed it. A parked Shared row stays
  where it is, dimmed for you alone; Parked also lists a parked Shared row,
  which opens in its space without a tab of your own. Screen readers hear “Parked”, “Parked
  until …”, or “Parked until it is done”. Close others, Close to the right,
  Close all, and Close group close this space's parked tabs too; Cmd/Ctrl+[
  and ] skip them.
- **Auto-park.** A session you auto-park parks its tab until it is done each
  time you send it a message: Send, Steer, a queued message, or a `$goal`, but
  not a stash or a command the server answers itself, such as `$agent`. For a
  remote OpenCode session it is each prompt you send from its view; one sent
  before RocketClaw has listed the session does not park. Config's **Tabs**
  section has **Auto-park sessions**, your choice for every session, on until
  you turn it off, and a
  session tab's menu, Shared rows included, offers **Auto-park
  when I send** or **Stop auto-parking** for that session alone, which then
  wins over the Config choice. Both are yours alone and kept by the server, so
  they follow you to every device. A message queued behind a running turn
  parks until that turn ends, not until its own does.
- **Attention follows you.** Bold and parks are yours alone, kept by the
  server for every kind of tab, so they show on every device. Closing a tab
  keeps its mark, a park with a time, and a park until it is done; closing a
  tab parked until you unpark it ends the park. When a park time comes due on
  a closed tab, or the turn its park waited for ends, the tab comes back where
  it was: in its space, in its group when that still exists, and at its old
  place, bold with a notification. The palette offers every item above as
  **Tabs: …** for the tab in view, such as **Tabs: Park for 1 hour** and
  **Tabs: Park until I unpark**. New session has none of these items.
- **Reordering.** Drag a tab onto another in its card. With a tab focused, Shift+Arrow moves it.
  A moved tab joins the group of the tab it lands on, or leaves its group when it
  lands on an ungrouped tab. Drag a group's header, or press Shift+Arrow on it, to
  move the whole group before or after the tab or group it lands on. A group
  header is also a drop target, standing for its first tab, so a collapsed group
  can be passed or joined.
- **Groups.** Named, colored groups of adjacent tabs, as in Chrome. The tab menu's
  **Add to group** submenu, like **Move to space**, offers **New group** (an
  unnamed group in the next color, with its editor open and the name field
  focused) and each other group by name; the menu also offers **Remove from
  group**. The command palette offers the adds as **Tabs: Add to new group** and
  **Tabs: Add to group "…"**. A group's header sits before its tabs: a
  colored chip with the name, or a color dot when unnamed. Clicking it collapses
  or expands the group. Right-click, Shift+F10, or the header's **…** button opens
  the editor: name (saved on Enter or leaving the field), nine colors, **New tab
  in group** (moves New session into the group, or opens it there), **Ungroup**,
  **Close group**, which closes the members by the usual close rules, and
  **Delete group**, which closes them the same way and removes the group. A
  group stays when its last tab closes or leaves it: an empty group stays
  where it was, across reloads and devices, and shows a **New session** row
  that starts a new session in it. A tab dropped on that row or on the group's
  header joins it. Ungroup and Close group leave the group empty; only Delete
  group removes it. Members
  sit under the header behind a colored rule. A tab opened while
  a grouped tab is active joins that group. A collapsed group still shows its
  active tab, and activating any hidden tab, including with Cmd/Ctrl+[ and ],
  expands its group. Arrow keys skip hidden tabs. Headers report
  `aria-expanded`.
- **Spaces.** Spaces are shared by everyone except people limited to spaces
  (below): the server keeps one list, oldest first, starting with **Main**, and
  names are unique ignoring case. Every tab
  belongs to one space. The strip is one list of cards from every RocketClaw
  server: each server's Shared card (see Shared below) and one card per space.
  You put the cards in your own order: drag a card's title onto another card,
  above or below its middle, or press Shift+ArrowUp or Shift+ArrowDown on the
  title. A card you have not placed yet, such as a new space's, comes last, in
  its server's order. With more than one server, a card's title names its server
  after the space, as in "Main · hally": the page's host by its first label
  (`hally` for `hally.tail1234.ts.net`), and every remote by its configured name.
  A narrow strip cuts the server's name before the space's. Every space's card
  is drawn and works the same, whichever space is current and whichever server's
  page you are on; only the current space's card is tinted. Its title shows an
  open folder, or a closed one while the card is folded, its name, "· offline"
  while its server does not answer, a spinner while a turn runs in one of its
  tabs, and a blue count of its tabs waiting for you; clicking the title folds
  or unfolds the card. After the title come **Needs you** while one of its
  sessions asks you something, and a **…** button for the space's actions. Its
  body holds the space's tabs and groups, which work as described above in every
  card, another server's included: open, close, select, reorder within the
  card, and take their full menu. Double-clicking a card's empty area starts a
  new session in its space. Changes to another server's cards save on that
  server, and opening one of its tabs moves the page to that server. Every
  card starts unfolded; a folded card lists only its tabs that wait for you or
  ask you something, and the tab in view. Your card order and which cards and
  Parked branches are folded are saved on the local server, so they are the same
  on every server's page and every device. The **…** menu offers **Go to
  space**, which enters the space (another server's space opens in that
  server's view), then **Rename space** and, while the server has more than one
  space, **Delete space**, which change the space for everyone and which someone
  limited to their own sessions does not get. Renaming edits the name in place
  of the title: Enter or clicking away saves, and Escape, a blank name, or a
  name another space has keeps the old name. Opening a tab also enters its
  space. The command palette's **Spaces: New space**, or **Spaces: New space on
  <server>** for each server when there are several, asks for the new space's
  name and makes it on that server; a name another space has, ignoring case, is
  refused. It does not enter the new space.
  Entering a space returns to the space's last active tab, or to its first
  tab. New session is one tab, so entering an empty space, opening New session, or closing a space's last tab
  moves that tab into the current space. Opening a location that is open in
  another space switches to that space. The tab menu's **Move to space** submenu
  lists each other space, and moving the active tab follows it; the command
  palette offers the same moves as **Tabs: Move to "<space>"**. Moving a tab is
  your own: it moves only in your tabs. Deleting a space moves its tabs to the
  first space; the last space cannot be deleted. Close others, Close to the
  right, and Close all act only within the tab's space, and Cmd/Ctrl+[ and ]
  only within the current space. Screen readers hear each tab's group, if any,
  and space as its description, such as “Group Work, Space: Main”.
- **Agents and crons in spaces.** An agent or cron whose frontmatter sets
  `space:` belongs to the space of that name, ignoring case; a cron without one
  takes its agent's. The Cron page and Run cron palette hide crons, and their
  runs, that belong to another space.
- **Birth space.** A session is born in its agent's space; a Run cron session, a
  cron's report chat, or a chat continuing a cron run in its cron's space, else
  the cron agent's; any other Web session in the space you were in. A space named
  this way that does not exist yet is created with the session. A session opens
  in the space it was born in, and after its first message a new session moves
  there and takes you along. When you have moved a session's tab elsewhere, its tab
  menu offers **Restore to "<space>"**. Sessions from before birth spaces, and
  sessions started outside the Web other than cron report chats, have none and
  open in the current space.
- **Agent picker.** New session offers every agent. Once a session's agent has a
  space, its picker and `$agent` offer only agents with that space, ignoring case;
  when that leaves none the Slack channel allows, they offer all of those.
- **People limited to spaces.** A person the config's `permissions` lists (see
  [Space permissions](../../../README.md#space-permissions)) gets only their
  spaces from the server: the space cards, **Move to space**, and the Shared
  card show only those, and so do the session list, search, the agent
  picker, the Cron page, and the Page Index. Their space cards' **…** menu
  offers only **Go to space**, and the command palette offers no **New space**
  on that server, because the server refuses making, renaming, and deleting
  spaces from them. When another person switches a session to an
  agent outside their spaces, the session stays open to them, a pending question shows no answer form, and
  Send first switches the session to the agent the picker shows, one of theirs.
  A tab they saved earlier for a session outside their spaces opens into a
  refusal. The Config page hides its Runtime, Models, MCP, and Flags sections
  from them, since the server sends them only their Tailscale login and Slack
  channels.
- **Groups in spaces.** A group lives in one space. The tab menu offers only the
  tab's space's groups, and moving a tab to another space takes it out of its group.
  The group editor adds **Move group to "<space>"** for each other space: it
  moves every member and keeps the group's name, color, and collapsed state, and
  moving the active tab's group follows it. Deleting a space moves its groups,
  intact, to the first space. **New tab in group** brings New session into the
  group's space as well as the group.
- **Shared.** Every space has its Shared tabs, the same for everyone. The tab
  menu and the command palette offer **Add to Shared** and **Remove from
  Shared**; both change the tab's space's Shared tabs for everyone in the
  space, one tab at a time, so two people's changes never undo each other. New
  session and sessions no Web user may open cannot be shared. Each server has
  one **Shared** card, which has no **…** menu and lists every space's Shared tabs
  in your own order, newly added ones last, under a grey pill naming the space
  when the server has more than one space; it is hidden while the server has
  no Shared tabs. A tab can be in Shared and among your own tabs
  at once, and both rows light up while it is open. Opening a Shared row adds no
  tab of your own and opens it in its space. Shared rows show a people
  icon instead of a close button, and their menu offers only **Copy link**,
  **Remove from Shared**, the attention items (Mark as read or unread, Park),
  and, for a Search page, **Rename search** and, on the page's server,
  **Duplicate**; middle-click and selection never act on them. Drag a Shared
  row onto another row of its space, or press Shift+Arrow on it, to reorder it
  for you alone; your own tabs and
  Shared rows never drop on each other. Otherwise the keyboard treats them as
  tabs: Cmd/Ctrl+[ and ] step through the current space's Shared rows and then
  your own tabs, each tab once, and arrows, Home, and End cross between the
  lists. Your Shared order follows you across devices. The Shared card folds
  and moves like any card. When the Shared tab you are viewing is removed, the window
  enters its space as it does for a tab closed elsewhere. Other people's changes arrive
  when a window regains focus. Deleting a space moves its Shared tabs into the
  first remaining space's, and deleting a session takes it out of every Shared
  group.
- **Running turns** make the tab's title shimmer with a moving gradient, so the
  title stays in place. With reduced motion the title shows a still gradient
  that fades toward its end. Screen readers hear “Turn running” in the tab's
  description.
- **Background work** that can wake an idle session swaps the tab's chat icon for an
  hourglass: a running Background Job, its own or that of a hidden run reporting to it, or a
  remote OpenCode session's running child session (a background subagent) or shell command.
  A running turn keeps the chat icon. Screen readers hear “Background work running”.
- **Unread** tabs show a bold title, and any kind of tab can be unread. The
  server marks a tab when something new happens on it: for a session, a turn
  ends, finished, failed, or stopped, or the agent starts asking a question;
  for a Search page, a search you entered finishes; for a cron's page, its
  run ends, once nothing is left to wake it, and the Cron page of the cron's
  space is marked too; for
  a published page, an HTML file of it is stored, and the space's Pages list is
  marked too. Agents, Skills, Settings, Shortcuts, Voice, and Recently closed
  are marked only by you. A session's turn or question reaches you, with a
  notification, when you sent or steered that turn from the Web, when the
  session is in your own tabs in any space, or when it is Shared in a space you
  can see, as a Shared tab is a tab of everyone's; whoever started the turn,
  Slack mentions and crons included. A Cron or Pages tab reaches you the same
  way when it is your own tab or Shared. A
  remote OpenCode session's turn counts when it holds a prompt. Nobody is
  marked for the tab they had in view in the last 10 seconds, or the last 4
  for a question or a timer, and nobody is marked for a space outside their
  limit. You can also mark a tab yourself (see Mark as read and Mark as unread
  above). Having the tab active in a focused window clears the mark on all
  your devices; the server keeps it for you, even while the tab is closed, so
  it waits for you after a reload or on another device. Screen readers hear
  “Unread” in the tab's description.
- **Keyboard.** The strip is a vertical `tablist` with one Tab stop. Arrow keys (Up/Down
  or Left/Right), Home, and End move focus; Enter or Space activates. Anywhere in
  the app, Cmd/Ctrl+[ and Cmd/Ctrl+] activate the previous and next tab, wrapping
  at the ends. This replaces the browser's Back/Forward for those keys.
- **Sidebar.** Tabs sit in a 14rem sidebar on the left. Drag its right edge, or
  focus the edge and press Left/Right, to resize it; the width is kept in this
  browser. The **Hide sidebar** button at the footer's left end or Cmd/Ctrl+B hides
  and shows it until the next reload. Below the `md` breakpoint, the sidebar is a
  drawer: the same footer button or a swipe right opens it, and picking a
  tab, tapping outside, Escape, or a swipe left closes it. A swipe that starts on
  a text field, a slider, or something that scrolls sideways is left to that element.
  While the sidebar is a drawer, a bar at the top of the page shows the current
  tab's name, as its tab shows it.
- **Storage.** The server keeps each person's tabs, so they follow you to every
  device: open tabs, their order, each tab's group and
  space, the groups' names, colors, and collapsed state, the current space, and each
  space's last active tab. Each change saves the whole state,
  one save at a time. A save made from tabs older than another window's save is
  refused; the window then merges in that newer save, keeping what it opened,
  closed, or changed itself, and saves again, so neither window's changes are
  lost. The server keeps one row per tab and per group. A session's tab is saved
  only while the session exists, and deleting a session removes its tab for
  everyone. Every group holds its place with a hidden tab row whose path is
  `group:<id>`, which no tab command sees; a group saved before every group
  had one gets it after its last tab. Groups with no tabs, that row included,
  are dropped, except the entry that keeps whether you collapsed Parked. Tabs whose space is gone join the
  first space keeping their group, and tabs whose group is gone leave it. A
  window reloads them when it becomes visible or regains focus: it takes in
  changes made elsewhere, merged with its own unsaved ones, but stays on its own
  tab, unless that tab was closed elsewhere. The address is the active tab, so a
  reload keeps it, and opening any URL, including Home (`/`), activates its tab or adds one. Tabs once kept in
  `localStorage` are not carried over. Titles and running state come live from
  the session list; a session missing from it shows its ID.

Tradeoffs: every opened location keeps its tab until closed, so nothing vanishes
unexpectedly, but the strip grows until Close others, Close to the right, or Close
all trims it. Unlike browser-style tabs there is no
per-tab Back/Forward or reopen-closed-tab. Unlike grouped tabs there are no
agent/age lines. Every space's card lists all its tabs until you fold it, so a
tab in another space is at hand, but keeping one task in focus means folding
the other cards. Unlike warm tabs, switching sessions remounts the
transcript. In exchange, the strip stays compact, and the menu and drag
give more control than a minimal strip.

The session list restores the last complete list only after live identity confirms the
Go-mapped username, keys native IndexedDB by owner and protocol, and keeps that
snapshot until a refresh is successfully exhausted with complete summaries.
Each row previews the latest nonempty user or assistant message, including delivered
assistant reports. A small spinner in the metadata line indicates a running turn
without moving the row; saved snapshots do not restore an old running indicator.
Routine background refreshes are silent. When rows are stale, the icon inside session search
changes to a same-size status indicator, with “Conversation list may be out of
date” on hover or keyboard focus. No extra space is reserved and the list does
not shift. Missing summaries show `loading...`.
Failed, partial or cancelled streams do not replace the snapshot. History
deletion clears preview text without removing the row. Composer choices and
sends stay on the agent query and do not wait for session-list refresh.
Click or tap the handle to hide or show the bottom navigation, or swipe the handle
down to hide it and up to show it. On mobile it floats over a slim 16px strip while
retaining a 44px touch target. The handle stays
visible when the controls are hidden and supports Enter/Space when focused.
Touch devices and narrow screens use 48px navigation targets and 8px gaps;
desktop uses 32px targets and 4px gaps. Screens narrower than 22rem keep 48px
targets but tighten gaps and side padding to 4px. Icons are 24px in all layouts.
The sidebar toggle sits at the bar's left end and the right pane toggle at its right end;
between them, centered, are New session, Search sessions and commands, Voice, and
Copy page link.
When remote RocketClaws or OpenCode servers are configured, New session opens a
menu of where to start one, with the page's own server highlighted so Enter starts
it there. The menu names the local server by the first label of the page's host
(`hally` for `hally.tail1234.ts.net`), or by the whole address for an IP address.
Another RocketClaw opens its New session page, and an OpenCode server opens the
palette's folder picker, as **Sessions: New on <server>** does.
Cmd/Ctrl+Alt+N opens a new session on the page's server, without the menu.
The magnifying glass beside New session opens
the palette, like Cmd/Ctrl+P. The **Sessions: Search** command opens
the Search page. Each search has its own editable tab, saved in this browser for
the confirmed user. Click another tab to switch searches; click the selected tab
to edit its name in place. Enter or clicking away saves; Escape cancels.
Custom names stay unchanged when the query is edited; a blank name
restores the query label. Results use a compact Search Editor-style list, grouped by
conversation. The match count sits in parentheses beside each title, followed by
highlighted, clickable matching excerpts. Excerpts use the message formatting
above without inner links or code-block buttons, so a click opens the match;
code in an excerpt wraps to the row width.
With a search word, an excerpt shows the matching line with up to two lines on
each side, widened to keep a code block whole; without one it shows the whole
message. Message search also matches Slack tags by name: `cs-operators` finds
messages that tag `@cs-operators`, and the tag is highlighted. Message matches
jump to the recorded user or assistant message, including after a reload;
other matches open the conversation and are labeled with the field that matched
(Name, Room, Agent, Session, Origin, or Preview). Conversations with message
matches come first, pinned conversations first within each group.
An empty search asks for a query instead of listing conversations. Closing a tab
removes that search; closing the last returns to the last message visible in any chat, or Home
if none was seen. Opening Search again starts an empty tab. The address follows the
selected tab as `/search?q=…`, omitting an empty query and adding no
history entries, so it can be copied and shared. Opening such a URL selects a tab
with the same query, or saves a new one, so Back from a result
reuses that tab. Older links and saved searches with `agent=` or `room=` filters are
read once as the query `agent:<value> room:<value> <query>`, quoting values that
contain spaces, and saved searches are rewritten that way. Cmd/Ctrl+P still opens
the palette, which lists every conversation. It is the quick look and the
Search page is the deep dig: the palette matches only the title a row shows (its
name, else its preview's first line), as it reads on screen, so `**Done.**` matches
"done." and a Slack mention matches the name it shows. It never matches message
text or other fields, and lists each conversation once. A match past the start of
a long title shows the title from just before it, highlighted. Typed text ends the list with
**Search in this space for '…'** and **Search everywhere for '…'**. Both open the same
Search screen in the space where the palette opened. The scoped action prefixes an
editable `space:<name>` filter; everywhere adds no space restriction. Each reuses an
exact-query Search page only in the opening space, otherwise it creates one there.
Explicitly choosing a saved Search-page match still enters that page's own space.
Each dialog opening clears text, pills, and result selection and focuses
the input.
Once text is typed, matching commands appear above the conversations; an empty
query, or one with a filter such as `tag:customer` or `is:pinned`, lists
conversations only. A bare key such as `cron:` is not a filter. Commands match
case-insensitively: a label equal to the text comes first, then labels containing
it, then, once three or more characters are typed, labels containing them in order
with gaps. Within each group the command order below applies. Enter runs the
highlighted command at once when its label contains the text or it was picked
with the arrow keys; otherwise Enter opens the first conversation, as above, and
plain text that matches no conversation opens Search everywhere. When only the two
search actions remain, everywhere is highlighted; ArrowUp selects scoped search.
Clicking a command always runs it.

**Duplicate**, in a Search page's tab menu
or Shared row menu and in the palette as **Tabs: Duplicate**, opens a copy at a new
address with the same searches, named "<name> copy" (an unnamed page
after its shown query, as "<query> copy"). A copy of a Shared page is added to
its space's Shared tabs; any other copy is your own tab.

Cmd/Ctrl+P matches plain text against titles in the browser as you type, with no
request. A query with a filter, even a bare key such as `principal:`, goes to the
server, which applies only the filters; the palette matches the rest against the
titles of the conversations that pass, the same way as plain text. A failed
filtered search lists no conversations.
The Search page sends every query to the server. Queries are sent once typing
pauses for 250 ms (the Search page also at least once a second while typing),
Enter sends one at once, and a newer query cancels the older request. A Search
page's search runs on the page's server past the request: leaving the page only
stops waiting, the search keeps running even with the window closed, and the
page receives the result when you come back. A search you entered by typing,
Enter, Retry, a link, or the palette marks the tab bold with a "Search
finished" notification when it ends while you are elsewhere; reopening the page
runs it again without marking, and new sessions in the list start it over, still
marking once if you entered it. Other servers'
results are fetched while the page is open.
Results refresh when conversations appear in the session list. An empty
Cmd/Ctrl+P lists the session list without a request; it opens with the first 50
rows and renders the rest in the background. Until new results arrive, the
previous list stays visible, dimmed (in Cmd/Ctrl+P, the title matches for the
text without its filters), and a spinner replaces the search icon. With nothing
to list, Cmd/Ctrl+P shows “No matches” or loading feedback above Search
everywhere. Enter in Cmd/Ctrl+P on a filtered query opens the first result for
the text typed so far, waiting for it if needed; with no result, or after a
failed search, it does nothing.
The Search page matches names, titles, previews, agents, session labels, origin
card fields, and original MCP metadata keys and values, case-insensitively, as
one phrase.
`agent:main` and `room:general` are filters in the query text: exact,
case-sensitive matches on the agent or on a Slack thread's room; quote values
with spaces, such as `room:"general chat"`. Typing `agent:` or `room:` suggests
values; choosing one by click, arrows, Tab, or Enter writes the filter into the
query without replacing earlier agents or rooms. Multiple agent filters match
any listed agent; multiple room filters match any listed room. Other filters
still narrow those results. `agent:` also matches a chat that shows a hidden
cron or External MCP run of that agent, such as `agent:dream` for the chat a
`dream` cron run reports to.
A remote OpenCode session's agent is the one it has selected, so `agent:build`
also finds those. `server:box` keeps the remote sessions of the configured
OpenCode server `box`, exactly. `model:opus` keeps conversations whose model
contains the value, ignoring case: a remote session's selected model, such as
`anthropic/claude-opus-4-7`, or a RocketClaw chat's agent's configured model.
Conversations whose whole model equals the value come first. Typing `server:`
or `model:` suggests the servers and models in use, and typing `agent:` also
suggests remote sessions' agents.
`space:"Blue garden"` matches a session's complete Birth Space name, ignoring case,
not where its tab currently sits. It excludes sessions with no Birth Space;
everywhere includes them. Typing `space:` suggests every existing space, even empty
ones. Names use the same JSON quoting as other filters. Saved queries keep their
name text after a space rename; edit the filter to use the new name.
Suggestion keys take priority over results; Escape dismisses suggestions first,
then closes the dialog. Both Cmd/Ctrl+P and the Search page accept exact,
case-sensitive tag filters: `tag:customer`, or `tag:"Needs review"` for a name
containing spaces. Quoted values use JSON escaping, such as `tag:"say \"hello\""`.
`principal:"Writer One"` keeps conversations containing a visible human prompt
with that exact recorded principal. Values are case-sensitive; the filter key
is not. Attribution comes from the recorded prompt header, not session ownership
or text that looks like a header. Typing `principal:` suggests recorded values.
Repeated principal filters require every named principal to appear somewhere in
the conversation. Other filters and search text still apply. Principal search
uses the message index and follows the same history cutoff and indexing status.
Other repeated filters, except `agent:` and `room:`, use AND: `tag:customer tag:triage` requires both tags. Unknown
tags match nothing. Bare `tag:`, unclosed quotes, and invalid quoted values stay
ordinary search text. A filter-only query lists every conversation it keeps;
`tag:customer outage` keeps message hits only from tagged conversations.
Filters constrain both row and message matches. The session-search dialog lists each conversation
once, pinned first, retaining recent-first order within each group.
`sort:newest` or `sort:oldest` instead orders results by last activity on every
search surface, overriding pinned-first and message-hit grouping; rows without
activity come last, equal times order by conversation ID, and the last `sort:` wins.
Typing `is:` suggests `pinned`, `forked`, `cron`, and `unread` (sessions bold for you, closed tabs included, parked ones not),
then the work terms: `running` (a running turn or background work), `queued` (waiting turns other
than scheduled reminders), `waiting` (a session marked Needs you: a running turn's unanswered
question, or a remote OpenCode session's pending form or permission request), and `external` (a
chat bound to External MCP). `running`, `queued`, and `external` count the hidden runs reporting to
a chat, such as its External MCP run, as the chat's own work, and agents' `rocketclaw_sessions_list`
reads every work term too. Each listed
session also carries when its work state (the first of needs you, running, and queued, else idle)
began, which comes from the server and reads the same for everyone. Typing `sort:` suggests
`newest` and `oldest`. While typing, a last word that is an unfinished filter
(a bare `tag:`, `cron:`, `agent:`, `principal:`, `room:`, `server:`, `model:`, or `space:`, a partial `is:` or `sort:` term
such as `is:pin`, or an unclosed quote) is not sent; Enter, opening a saved
search, and following a link send the whole query. Unknown complete terms such
as `is:foo` stay ordinary text. Filter pills show the server's reading of the
query: a filter becomes a pill once it is followed by a space and the results
for it arrive. The word being typed stays in the input, and removing a pill
removes exactly that filter. Editing the text never removes a pill or joins
text to it; a pill inside or right after the edited text moves in front of it.
Pills show the full filter text, wrapping long values to fit the available width.
A conversation the server finds before the session list has loaded it shows
loading feedback rather than “No matches”. Until the server finishes indexing
existing chats, the Search page and a filtered Cmd/Ctrl+P query say message
results may be incomplete, and until every chat has a summary they say results
may be incomplete; the Search page says so instead of reporting “No matches”. A message
hit from a stopped or failed turn opens its chat without jumping to a message. A
failed search shows an error with Retry, and the next keystroke also retries.
Cmd/Ctrl+Shift+P opens the same palette with commands only. It always offers
**Sessions: New**, **Sessions: Search**, **Voice**, **Cron: Dashboard**, **Cron: Run**,
**List Agents**, **List Skills**, **Settings**, **Tabs: Previous**, **Tabs: Next**,
**Tabs: Recently closed**, and **Help: Keyboard Shortcuts**, plus **Tabs: Toggle sidebar** while Cmd/Ctrl+B
works (the desktop sidebar, not the narrow-screen drawer), and **Tabs: Reopen closed tab** while a tab is closed. In both palettes a command bound to a
key shows it on the right of its row, OpenCode style: **⌘ Option N** on Mac or
**Ctrl Alt N** elsewhere for **Sessions: New**, **⌘ [** and **⌘ ]** for the tab
commands, **⌘ Option T** for reopening, and **⌘ B** for the sidebar toggle. Unbound commands have no shortcut
hint. **Help: Keyboard Shortcuts** opens the read-only `/shortcuts` page, which
lists every shortcut from the same list: the palette keys, new session, tab
stepping and reopening, the sidebar toggle, Escape on a page tab, and the message box's steer
and stash keys.
Timeline detail levels are also available, from `Timeline: Messages only` through
`Timeline: Everything`.
Like VS Code, selected commands appear first, newest first;
unused commands follow alphabetically, without section dividers. History stays in
this browser across reloads, stores command IDs rather than session IDs or draft
text, and records only selections—not searches or dismissals. Search still filters
the list, and unavailable session commands stay hidden even when recently used.
Rows keep compact spacing and touch-sized targets on mobile.
For the current session it also offers **Open original conversation** (when forked),
**Name session**, **Regenerate title**, **Pin/Unpin**, **Fork session**,
**Handoff session**, **Choose agent**, **Delete session**, and **Stop turn** while running.
Fork, handoff, rename, and agent selection open their controls directly.
For a remote OpenCode session it offers **Copy server link**, which copies the
shown session's address on its own server (the server asks whoever opens it to
sign in), and, while the view is open, **OpenCode commands**, which opens
OpenCode's palette.
**Command: Start goal ($goal)**, **Command: Invoke skill ($skill)**,
**Command: Enqueue work ($enqueue)**, **Command: Stash work ($stash)**, and
**Command: Steer turn ($steer)**
prefix the corresponding `$command` to the current draft and focus the composer.
Existing draft text and attachments are kept; nothing is sent until you submit.
In web, `$enqueue <text>` queues automatic later work, `$stash <text>` holds work
until explicitly sent, and `$steer <text>` sends now or guides the running turn.
Only the outer delivery command is removed; inner commands and text are preserved.
`$goal <objective>` starts a goal
loop on the conversation, with a `maxTurns:` option; its first turn runs right away when idle, otherwise after the active turn.
Bare `$goal` privately shows usage, and `$stop` ends the goal.
The Stash button and shortcut still hold the entire draft literally, including
any `$command` text. Slack has no commands: `$` text in a Slack mention reaches
the agent as ordinary text.
Run cron opens a new session immediately, titled by the cron's name until its first message arrives. The job still runs privately, but its turn shows live in the chat, with **Working…**, and is replaced by the copied history when it finishes. The same holds for every hidden cron or External MCP run that reports to a chat.
A chat that began as a cron run or an external MCP call starts with a collapsed origin card. Use show more to see its details; the card scrolls with the messages. An ordinary chat has no origin card.
The composer's session buttons appear only on desktop; on mobile, the command
palette offers the same actions.
Safe-area padding is preserved. On very narrow screens, navigation buttons scroll
horizontally instead of overlapping.

The composer follows the embedded OpenCode layout: a rounded
[Prompt Input](https://ui.shadcn.com/docs/ai-elements/base/prompt-input) card with
pending files above the text and a scrolling agent, model, and effort row below.
Model and effort describe the selected native agent; they are not independent selectors. Use its
agent menu, a [Model Selector](https://ui.shadcn.com/docs/ai-elements/base/model-selector),
to change agents; each agent shows its model and effort, and typing a name's first
letters moves to it. New Web threads select
`main` by default, or the first listed agent when `main` is not listed; existing
threads retain their agent. When a thread's agent is no longer listed, for example
because its Slack channel does not allow it, the selector shows the channel's first configured
agent (or the first listed agent elsewhere), and your next message, a popped stash, or queued work you send from the
queue switches the thread to it first.
Stash, `$stop`, and a typed `$agent` do not switch.
New chat opens a fresh Home composer and creates a session only on the first send.
It clears the draft and resets the agent to `main`; returning from other pages
preserves the current draft and selected agent.
Select text in the chat and click **Quote** to append a Markdown blockquote to
the current draft. The composer adds a blank line after the quote and places the
cursor there for your comment. The browser's right-click menu remains available.
Use **+** to choose files, drop files onto the composer, or paste them, such as a
screenshot copied to the clipboard. Pending files stay in
selection order and can be removed individually. Send accepts files without text;
On desktop, Enter and **Send** queue later work while a turn is running.
The **Steer** action appears beside Send only with a draft during a running turn;
Cmd/Ctrl+Enter does the same. Empty drafts disable Send, or show **Stop** while working.
Phones and tablets use a separate bottom action bar with 44px touch targets:
**Send** when idle, **Queue** and **Steer** with a draft while working, or **Stop**
with an empty draft. Return adds a newline, even with command suggestions open;
Cmd/Ctrl+Return on a touch device performs the primary action. A narrow desktop
window keeps desktop behavior. Voice remains available beside the composer actions.
After sending, focus returns to the composer so the next message can be typed.
Steers wait in **Waiting to steer**, separate from both chat and the later-work
queue. A persisted input in a History delta moves that input into chat at the point
it was used, with its attachments. Identical texts remain separate inputs, tracked
by durable input ID. Queue promotion keeps the server queue ID and follows the
same consumption rule. Replacing the active turn includes each consumed steer
without repeating the earlier response.
Use **Composer actions (…) → Stash** to save the draft and its attachments without sending, whether the
chat is idle or busy. Press **⌘ Option Enter** on macOS or **Ctrl Alt Enter** on
Windows/Linux to stash, including when command suggestions are open.
**Stashed** rows stay in the queue panel until you choose
**Pop** or Remove, including across reloads and server restarts. Command-looking
text is saved literally. Pop moves the row to the end of the ordinary queue.
The button shows **Popping…** while releasing it, and waiting rows show **Queued**.
It may run immediately when idle. While busy, the released row offers **Steer**
as usual. Stashed rows can be reordered or removed, but cannot steer directly.
Each queued or stashed row has an **Expand message** control; activate it with
Enter or Space to read the complete original text, with line breaks, indentation,
and long words wrapped to fit. **Collapse message** restores the compact inline
preview. Expansion is read-only: it keeps attachments, order, delivery, actions,
and the composer draft unchanged. Long text scrolls inside the queue panel, even
with one row, so the composer stays on screen.
**Edit** opens a text box in the queued or stashed row. **Save** changes its text
without moving it or changing its attachments, delivery, author, or routing;
**Cancel** discards the edit. Neither action changes the composer draft.
Enter saves an edit without sending it; Shift+Enter inserts a line break.
The panel grows while editing so the text box, Save, and Cancel fit together;
long text scrolls within the text box.
While saving, conflicting row controls are disabled. A failed save keeps the
edited text for retry; a message already consumed cannot be edited or recreated.
Pending steers are not editable.
Failed Stash keeps the draft and files for retry. If Pop shows an error, check
the refreshed queue: the message may already be queued if starting work failed.
Live updates and refresh read the same persisted transcript. A chat opens with
its newest 50 turns, showing **Loading conversation…** until they arrive. Scrolling near the top loads the 50 before them without moving
what is on screen, and a link to an older message loads everything from that
message onward. Live updates follow only the turns loaded at open, and finished
turns are not redrawn. Reconnect fetches changes since the last applied revision; pending steers remain separate until their
input IDs appear in history. Reads are serialized and overlapping signals coalesce.
Changed entries replace their whole groups, deleted entries disappear, and unchanged
groups retain their render IDs. History confirms stored attachments by file ID,
replacing local previews with download URLs. Failed reads retain content and the
last applied revision. Recorded message IDs identify saved entry positions,
separately from render and input IDs; the browser never matches consumption by text.
Public assistant text appears when the provider emits it, before the request ends.
Tool and delegation status updates persist independently, without waiting for the
slowest sibling. Their rows keep the original call order. Private child diagnostics
and provisional tool arguments are not public progress.
Final text replaces partial text exactly, including when the final text is empty.
Stopped or failed turns retain their progress without becoming resumable work.
Progress does not create command targets or move waiting steers into the model early.
Slack still receives one in-progress placeholder and the final answer/attachments,
not intermediate Web content.
Steer is enabled while a
response is running and the draft has content. Failed uploads or
sends keep the draft and files for retry. Drafts remain in memory when switching
chats or pages; **New session** resets the Home draft. Reloading restores persisted
composer content after Identity and config are accepted. Sending is locked through upload and dispatch, preventing repeated
clicks or Enter presses from submitting the same pending files twice. The composer
then accepts another input while the original Prompt waits for its turn to finish.
If New-session Send resumes a sticky session, its old draft and files are stashed
first, without running them. A failed stash leaves both drafts intact; a failed
new send stays in the resumed session's message box with its files and error.
Opening an existing session without sending leaves its draft in the box.

History, queued messages, and live replies show attachment names and download
actions. Raster images have inline previews, including attachment-only replies.
Files retain their original bytes; previews do not resize or re-encode uploads.
HTML, SVG, and other active formats download instead of rendering in the app's
origin. An **Original unverified** label appears when supplied by the backend.
Typing `$skill` opens completion for the selected agent's allowed skills;
typing a name filters the list, and choosing a skill leaves room for arguments.
Send `$fork` to search the current session's recorded user messages, newest first.
Choosing one copies history **before** that message into a new session and restores
the selected text and attachments in its composer. **Full session** copies all
recorded history. The source remains intact, and the fork starts no turn until
you send a message. Active, unrecorded output and queued work are not copied.
Copied Completion Notes keep their rows and the scripts' retained oversized output,
which expires when the source's does; running Background Jobs stay with the source.
Forked sessions, remote OpenCode forks included, show a fork glyph in session rows,
and in their tab in place of the chat icon. Use **Open original conversation**
in the command palette to return to the
immediate source, or search `is:forked` to find forks.
Session rows show the title, Slack channel when available, agent name and active
tags, fork/running indicators, and relative time. Metadata text starts at the row's
left edge, without a spinner indent. Tags render as plain text; the metadata title
retains long labels. Rows update even when only tags change.
Owner/protocol-scoped snapshots retain tags; older cached rows without tags stay
usable. Labels follow the existing two-second refresh cycle and do not change
ordering or incomplete-search feedback.
The source link is stored for new forks; older forks created without this metadata
cannot be identified retroactively. Deleting a source leaves its forks intact.
Send `$handoff` from any session to open a search with **Copy handoff** and
**Start new session** always available. Type to find a destination by message;
no destination search runs before you type. Handoff generation begins in the
background when the dialog opens. An early choice waits for the document; closing
without a choice cancels generation. Copy puts the document on the clipboard
without starting a turn. Start new session sends it as the first turn, or resumes
the sticky session after stashing its old draft and files in the same way as Send.
Select a search result to load its session beneath the dialog. Inspect the
destination and document, then choose **Stash handoff here**. The document waits
in that session's stash for **Pop**; stashing starts no turn. Cancelling the
dialog stashes nothing. Generation uses the source agent with tools disabled
and leaves the source history intact.
The handoff includes the source session ID and points to `rocketclaw_sessions_get`,
called inside Execute, for reading more details from that session.
After choosing a destination, **Copy Handoff** also copies it without stashing.
The handoff panel keeps **Preview**, **Copy**, and **Stash** actions visible.
**Preview** opens the full document; selecting a destination shows its conversation
below the panel and hides the composer until the handoff is closed.
Conversations without stored history stay out of the session list until their first
entry arrives, except while a turn runs: a new session is listed during its first
turn, titled by the start of that message, so it can be renamed before its first reply.
Clearing all stored history hides the conversation again once no turn runs, without
deleting its record or its Slack messages.
On desktop, the page header's close button closes that page's tab (see
[Tabs](#tabs)), and Escape on a page does too unless a dialog or tooltip is open.
New chat and search form a centered group in the footer.
The **Theme: Auto**, **Theme: Light**, and **Theme: Dark** commands, in both command palettes, and Config's **Light or dark** select switch between following the system, light, and dark. Config also has a select for the color theme. The choices are Neutral, Harbor, Grove, Ember, Violet, Rose, Sand, Lagoon, Slate, Copper, Ink, Signal, Go - Playground, and Go - Sources. Ink and Signal are high contrast. The Go themes adapt Mike Gleason jr Couturier's light themes, with local dark variations. The choice is stored in the browser, not on the server. See [theme sources and licenses](../../../THEMES.md) for attribution and reuse terms.
Config's Timeline section has the **Timeline detail** card, following OpenCode v2. A
five-stop slider picks Messages only, Quiet, Compact, Detailed, or Everything, and an
Advanced table sets visibility, Group, and Collapse per category; settings that match
no level read as Custom. Everything expands every tool call, thinking trace, and notice,
and every category has a Collapse switch. A browser that never chose shows Compact. The setting is saved
in this browser under `timeline-detail`, applies to every open timeline at once,
including other tabs, and is not sent to the server.
Messages only hides failed tool panels too, but shows a small “A tool failed” status
on the turn. Raise timeline detail in the command palette to see the failure details.
Delivered files and background-job rows remain visible. Other detail levels keep
their existing failure visibility.
The down-arrow appears above the chat composer when away from the latest message.
It scrolls to the latest message and resumes following live replies. Reading earlier
messages keeps automatic scrolling paused.
When other people type in a chat you have on screen, a pill floats at the top of the
conversation, centered, without moving it or taking clicks: the first typist's
initial, "Ana is typing" and animated dots, "Ana and Cy are typing" for two, all
three names for three, and "Ana, Cy and 2 others are typing" for more. Long names are
cut short so it fits the pane. Screen readers hear the sentence once per change,
without the dots, which stop moving under reduced motion. A typist leaves it when
their draft empties, by sending, a `$command`, an answer, or deleting, and 5 seconds
after their last keystroke report; you never see your own typing. Your composer
reports on the first keystroke, then at most every 2 seconds while you type, and once
more when the draft empties. Each report leaves at once, so one that hangs holds back
nothing, and the server drops one that arrives after a later one from the same tab.
Opening a saved draft and the New session page report nothing. Only the chat on screen
watches: a chat kept hidden on another page does not. In an OpenCode tab, a subagent
session the view opens takes no prompts, so its pill names who types in the tab's own
session: "Ana is typing in the main session".

An executable [PlusCal model](../../../docs/verification/typing/README.md), checked
with TLC by `make -C docs/verification/typing`, checks that you never see your own
typing, that a pill never follows you into another chat, that once you heard someone
stop, a late report never shows them typing again, and that the pill always goes once
their draft stays empty. A pill can still come back for at most 5 seconds when a viewer
never heard the stop: its stream was full, or it opened after the stop arrived.

Pin a session from the command palette.
Pinned sessions sort first and keep their normal recent-first order within that
group. Search `is:pinned` to find only pinned sessions; text and other
filters still apply.
The session age stays visible beside the agent; hover it for the exact
last-update date and time in your local time zone.
Use the rename icon beside the pin in the chat to set an optional name. Chat action
buttons have descriptive tooltips. Add files sits at the far left, before the agent
selector. On mobile, these and the session actions sit above Steer and Send. The name
replaces the session row's last-message preview without changing the messages; both
the name and the preview remain searchable.
When a person's first Web message starts a RocketClaw session that has no name, the
title model (`title_model`, `openai/gpt-6-luna#low` by default) names it from that
message in the background, as OpenCode does; a name set meanwhile wins. **Regenerate title** in the
command palette, or saving a blank name, names it again from its first request and
recent messages, and stays unavailable until that finishes. A failed attempt keeps
the old name.
Pins and names are shared with everyone who can see the session and persist
across reloads and devices.
Remote OpenCode names sync both ways: renaming here updates OpenCode, a blank name
or **Regenerate title** asks OpenCode to generate a new title, and a title changed
in OpenCode appears here on the next sync.
A failed OpenCode update leaves the local name unchanged.

The Cron page shows one line per cron, each name linking to that cron's journal,
where its runs are listed. Search above the grid filters the lines by case-insensitive
text matching across definitions, metadata, and run times. The grid distinguishes
recorded runs (filled cyan) from expected runs (outlined amber), with time labels.
The grid shows the past 12 hours and next 24 hours, including runs whose definition
is gone. Marker tooltips open on hover, keyboard focus, or tap; Escape dismisses them.
Each run is one run of the cron; its subagents and sessions are inside it. Click a
recorded run marker or the bar (for the latest run) to preview that run's own
reasoning and tool activity. **Open chat** opens the chat that holds
the run's output, or else creates or reuses a writable Web chat that shows the
run's history, without rerunning the job. Repeated opens return the same chat. It
offers every loaded agent, and the first loaded agent is selected when the chat is
created. Removed definitions keep
their recorded runs visible.
A cron's journal opens from its name on the Cron page or from **Cron: Timeline** in
the command palette, which lists every cron, retired ones too, with its run count
and last run. It opens in its own tab and lists the cron's runs, newest first under
a header per UTC day, each with the time since the run before it. Above them are the
schedule, a collapsed Definition with the cron's source and body, the run count, the
typical and longest gap, the last and next run, upcoming runs, and one bar per UTC
day; clicking a bar jumps to that day. The journal shows 60 runs, and **Show older
runs** adds 100 more. Picking a run previews it beside the list on a wide window and
under the run on a narrow one. If loading the runs fails, the page keeps what it
shows and retries until it loads. A play icon beside a cron with a definition, on its
Cron page line and in its journal, opens an in-app confirmation dialog
before running. It shows a spinner until
execution finishes, then opens the resulting chat.
Each turn shows its activity and replies in the order they happened. How much
activity appears is the browser's **Timeline detail** setting (see Config). Activity
falls into six categories: Execute (`execute` calls), Thinking (reasoning summaries),
Subagents (`task`), Skills (`skill` with its loaded instructions), Notices (developer
messages), and Other tools. Each category is shown on its own, grouped, or hidden.
Neighbouring grouped items of any category merge into one closed summary row such as
"Used 3 tools", "Thoughts", or "Updates"; a reply or a separately shown item ends the
row. Collapse decides whether Execute calls and reasoning start open; a summary row
of only reasoning shows the reasoning text itself when opened. Opening or
closing an item survives live updates of the same turn. Replies always appear. A
failed call (a result starting with `tool call failed`, `denied` or `aborted`) appears
on its own when its category is hidden, except in Messages only. A call that delivers files always appears
on its own. While a turn runs, **Working…** shows at every level. The chat has no
database-entry inspection panel.
Activity rows are shadcn/ui [Trace](https://ui.shadcn.com/docs/components/base/trace)
lines with an icon, a title, and a recorded state when there is one. Reasoning reads
**Thought**. A call recorded as working or in review, or one without a result yet
while its turn runs, shows a spinner and a shimmering title; a
failed or stopped call, or one whose result is an in-band failure, shows in red. A
summary row shows running if any of its items run, otherwise red if any failed.
While an `execute` or `task` call of an agent with
`permission.rocketclaw.allow_background: allow` runs, **Move to background** shows
beside **Working…**. It moves every such call of the conversation at once; the
agent gets a system note that you moved the work, and a script waiting on a
question loses that question and continues in the background. Work of other
agents stays in the foreground. Running Background Jobs, and finished ones whose
Completion Note the agent has not read yet, are listed above the composer, in the
queue's box, with their state and **Open**; running ones also get **Stop**. Prompts a
parent sends a busy subagent wait in that subagent's own queue. Open shows a subagent's Delegation
History, which lists that subagent's own jobs with Open and Stop, or scrolls to a
script's call, loading earlier turns when needed. Jobs
started by hidden cron or External MCP runs that report to this chat are listed
too, without Open. A `rocketclaw_sessions_write` call that awaits a reply is
also listed as a job until the other session's answer comes back; its Stop only
stops waiting for that answer and leaves the other session alone. Calls running
in the background stay visible at every Timeline detail level. Background Jobs
and Completion Notes are Trace lines too: a running job spins, and a failed or killed one shows in red. Each Completion Note is one
row, its label followed by `finished`, `failed`, `stopped by user`,
`stopped by agent`, or `killed (server restarted)`, shown at every level with the
full note collapsed beneath it. `$stop` does not stop
Background Jobs; use Stop.
When a turn started in Web calls `ask_user_question`, the question shows as a card
in the same box above the composer, and stays there after a reload or restart until
answered. The card is a shadcn/ui
[Questionnaire](https://ui.shadcn.com/docs/components/base/questionnaire): pick an
option, or check every option that applies for a multiple choice, then **Submit**. While the card shows, Send answers with the
composer's text as a custom answer instead of queueing it; Steer, Stash, and `$stop`
work as usual. If someone else already answered or the turn ended, the card goes
away and says so.
A tool row whose call delegated work (an `auto` permission review or a Task
subagent) shows **Open delegation**. A `task` row opens its
subagent, and a row that continued a subagent opens that subagent. Rows saved
before reviews had their own history open a review as **Open delegation**,
except a `task` row's. A call the approver reviewed, before it ran or during it,
such as a Code Mode script and each of its tool calls, shows each of the
approver's reasons as one line under its box, cut off until clicked. The shield
icon at the left of the line opens the permission review at the approver's
answer on that very call: each review saves its answer under a turn named after
the call it checks, so two calls given the same reason open their own answers.
Reviews saved before that open at the first answer giving the reason. Each link
opens its Delegation History read-only beside the chat on wide screens and full
screen on narrow ones, with breadcrumbs for nested delegations. The panel lives
in the `delegation` query parameter, the reason in `reason`, and the answer's
turn in `answer`, so Back, Forward, and reload keep it. It opens scrolled to
that answer, or all the way down when there is none, and has the same
scroll-to-latest arrow as the conversation. A link at the
end of each transcript goes back up one level. Only finished delegations are
saved. On wide screens the delegation panel can be resized by dragging its
inner edge, and the width is remembered.
Each tool call has one labeled disclosure containing its arguments and matching
result. Loaded skill instructions fold with their skill call. Long results have a
collapse control at the bottom as well as the header. An Execute call is titled
with the model's description of its script. One row per call the script made,
added when the call starts, shows the call's command and whether it was denied
or failed, with the approver's reason when it checked the call. Its output is not
shown. Chats from before calls were recorded list only the calls the approver
checked. The script's result follows as text. Under it come the approver's
decision and reason for the whole script, when `code_mode_approve` sent the script for
review, and any reason that matches no row, such as one saved after the turn
ended. Until the result arrives, those reasons show on their own. The Starlark source shows at the
Everything timeline level, behind a Show script toggle at Detailed, and not at
Compact. Files the script attached with `rocketclaw_attach_files_to_response` show
on the turn's final reply. Cron run previews and the
delegation panel keep every disclosure open.
The transcript has a visible scrollbar and a side rail with one jump marker per
loaded turn. Hovering or focusing the rail opens a scrollable box of message previews;
clicking a preview or its marker jumps to that turn. The box overlays the chat
without moving it, and scrolling the box does not scroll the transcript.
Each marker is labeled with its prompt; selecting it pauses automatic
following while you read earlier turns.
The shared app layout keeps the session list in memory during navigation.
Late storage reads merge with newer live rows; superseded saves cannot overwrite
newer snapshots, and post-deletion saves wait for the history clear to finish.

The saved-list tests use real Chromium and IndexedDB. Run them after
`bun run build`, with the installed Playwright module and Chromium executable:

```sh
export ROCKETCLAW_PLAYWRIGHT_MODULE='/absolute/path/to/playwright-core/index.mjs'
export ROCKETCLAW_CHROMIUM='/absolute/path/to/chromium'
bun test src/session-list.browser.test.ts
```

They check a 17 MiB snapshot across reload, user/protocol isolation, transaction
abort after request success, deletion winning over a delayed save, delayed
hydration, owner switches, and actual-App restore/merge/identity
behavior, including a connection cut after terminal metadata and protocol changes.

No browser test skips without these variables: the `src/*.browser.test.ts` files
and the Go-driven browser tests in `internal/rocketclaw/frontend/rpc` all fail
with `Cannot find package 'undefined'`.
See [Frontend RPC Tests Need Setup in Every Fresh JJ Workspace](../../../docs/solutions/best-practices/frontend-rpc-tests-need-setup-in-fresh-jj-workspaces.md)
for the setup.

## Attachment HTTP contract

The Go HTTP handler serves attachments alongside the protobuf JSON API and
`/stream`. TanStack Query owns ordinary query caches; session enumeration is a
finite SSE fetch at `GET /api/ListSessions`. A terminal snapshot is not transport
completion: only `event: complete` followed by successful EOF permits a saved
snapshot. Errors, cancellation, and EOF without that event fail enumeration.
`POST /api/Protocol` returns `{ "protoSha256": "…" }`; polling reloads the page
when the protocol changes. A web-only release leaves the protocol unchanged, so each
open page also fetches `GET /` once a minute and whenever a hidden tab becomes
visible. When that index page no longer names the page's own hashed script and
stylesheet, a bar at the top says a new version of RocketClaw is available and
offers **Refresh**; nothing refreshes without that click, and a rollback to the
page's build removes the bar. `GET /stream?id=<url-encoded-raw-id>` delivers only
content-free conversation-change hints via EventSource. Each hint triggers a History
delta read from the last applied revision; opening or reconnecting also catches up.
`GET /typing?id=<url-encoded-raw-id>` is the chat's typing stream, opened with
EventSource only while the chat is on screen: an empty `{"principal":"","typing":false}`
event first, then `{"principal":"<display name, or login when blank>","typing":true|false}` for other
people. The composer reports with `POST /api/ReportTyping` and
`{"id":"<raw-id>","typing":true|false,"tab":"<random per page load>","seq":<n>}`, never
the draft text, by plain `fetch`: `seq` counts the tab's reports from 1, and the server
drops one that arrives after a later one. A refused stream is not retried, and failed
reports show no error.

- `POST /api/UploadAttachment?conversationId=<visible-id>&name=<filename>` takes the raw
  file as its body and returns JSON attachment metadata. Query values use normal
  URL encoding, not the session route's base64url encoding.
- `GET /api/DownloadAttachment?conversationId=<visible-id>&id=<attachment-id>` streams the
  file. Safe raster MIME types use inline disposition; other types download.
  Add `&download=1` to force a download for an image too.
- `POST /api/Prompt` accepts `attachmentIds` in selection order alongside unchanged
  `id`, `text`, and `delivery`. The backend adds attachment references to the text.
- `POST /api/Reload` takes `{"reason":"updated runtime assets"}` and returns
  `{"message":"rocketclaw runtime assets reloaded"}`. It uses the same Web
  authorization and serialized, validation-before-swap reload as the agent tool,
  including External MCP agent refresh. It starts no agent turn, does not restart
  the process, and does not reread configuration. A refresh failure is reported
  after the assets have been replaced; it does not roll them back.
- `POST /api/Voice` takes `{"conversationId":"<visible-id>","sdp":"<offer>","voice":"<name>"}`
  (`voice` optional, one of the agent's `voices` from `ListAgents`) and
  answers with SSE: `{"answer":{"sdp","route"}}`, any `{"notice":"…"}` frames, then
  one `{"ended":{"reason"}}`.
  Errors before the answer are JSON errors. The call lasts as long as the request;
  aborting it hangs up. See the [transport README](../frontend/rpc/README.md#voice).

All JSON RPCs use PascalCase method names, camelCase protobuf fields, and
protobuf response envelopes. History includes `messages`, `revision`, `reset`,
`replacedKeys`, `removedKeys`, `entryKeys`, `running`, `terminal`, `start`, and `more`.
The chat sends `limit`, and older pages send `before` with `limit` or `from`.
Failures return `{ "code": <numeric-gRPC-code>, "message": "…" }`; authentication
failures use HTTP 401 and code 16.

Both attachment operations resolve identity from the actual socket's browser IP.
Forwarded headers cannot select identity. Download requests always contain the
**viewed visible conversation ID**, including cron previews and synced history;
metadata's `conversationId` can identify a private producer and is never used as
the download authorization target. Newly uploaded IDs become downloadable only
after the backend references them in history or the queue.

The Go handler streams HTTP bytes through gRPC `UploadAttachment`/`DownloadAttachment`,
using data frames of at most 262144 bytes and retaining default gRPC message limits.
The first upload frame has `conversationId` and `name`; subsequent frames contain
only `data`. Disconnects cancel the upstream call. Filenames use encoded
Content-Disposition parameters; responses include `nosniff` and `no-store`
caching. Protobuf JSON exposes camelCase fields and string int64 sizes.

The Go HTTP tests compare large files byte-for-byte in both directions and cover
request identity, metadata mapping, download headers, and cancellation.
The browser suite also exercises picker/remove/drop/paste, draft retention, failed-send
retry, attachment-only sends and live replies, inline images, downloads, and
queue versus steer behavior.
