# RocketClaw Web

Every page has a **Copy page link** share icon in the bottom navigation, on desktop
and mobile. It copies the full current address, including query and fragment,
and shows whether copying succeeded. Plain HTTP uses the same clipboard fallback
as message copying.

Assistant replies show a smaller, muted footer with known
`agent (provider/model#effort)` details; `#effort` is omitted when absent.
On desktop, hover a message to see its footer and Copy button. On touch
screens, tap a message instead. User messages show Copy and, when recorded, a
message-header info button, but no agent or model label.
Human messages also show their recorded author as a small, always-visible label
above the text inside the bubble. Queued/stashed rows and pending steers retain
their own author across viewers, reloads, and promotion. Rows another session's
agent wrote with `rocketclaw_sessions_write` show `agent <name> (session <ID>)`
as their author. The info control keeps
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

## Revert, Undo, and Redo

Recorded user messages in top-level pure Web sessions have a **Revert message**
action alongside Copy. Hover on desktop, tap on touch screens, or focus the
message with the keyboard. Slack, External MCP, Cron-origin and delegation
histories do not support Revert. Pending inputs keep their withdrawal controls.

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
for startup commands, browser IP mappings and the transport contract. The Go
handler takes identity from the browser connection; forwarded headers cannot
select a user. Serve it directly to browsers rather than through another HTTP
reverse proxy. Phones reach it over HTTPS through RocketClaw's own Tailscale node;
see [Voice](#voice).

## Voice

The **Voice** page (`/voice`, also in the bottom navigation and the command
palette) holds the signed-in person's voice conversation. RocketClaw keeps it, with the
person's agent, voice and transcript choice, on the server: the same on every device
and through restarts, and never shared with another person, even in one browser. Pick an agent and tap the orb to talk; tap it again to hang up. While a call is
active the orb slowly ebbs and flows, its inner light swirls around a glowing heart,
and two orbit rings spin around it; it swells and brightens with the voice model's
speech, measured in the browser from the incoming audio. With reduced motion it stays still. The first
tap creates the conversation and later visits, on any device, reuse it; each tap asks
the server first, so a conversation another device reset is never called. Picking another agent
while idle switches the conversation's agent with `$agent`. **Reset** hangs up and
forgets the conversation on every device, asking first while its turn runs, so the
next call begins a fresh one; the old one stays in the session list and search until
ordinary cleanup (an empty one is removed at the next restart), and a running turn
keeps running there. A call already live on another device keeps its conversation
until it ends. A voice picker next to the agent picker lists the voices the
agent's provider offers (ChatGPT-login and API-key providers offer different ones).
Each person has one voice: a call uses it when the agent's provider offers it, and
that provider's default otherwise, and the composer's voice button follows it too. **Sticky** (below) sits after the pickers. Icon buttons on the right show the transcript, open the
conversation, and reset. The row stays on one line: on phones the agent picker drops
its robot icon, and a long agent name is shortened. The page shows the call state, elapsed time and notices
under the orb. **Show transcript** swaps the big orb for live captions (not saved)
shown as a chat, with a small orb at the bottom that still starts and ends the call;
the choice is kept with the person's voice settings. Leaving the page hangs up, and cancels a start
still in progress; a conversation it already created stays.
The screen stays awake while the Voice page is open or a call runs.

Each session's composer has a voice button that starts and ends a call on that
session. It is not dictation: nothing is typed into the composer. It is unavailable
on New session until the conversation exists, and leaving the session ends its call.

A tab runs one call at a time, and each person has one live call: starting voice
anywhere else, on any conversation, ends their older call with "Voice moved to another
device or tab." Other people's calls, even on the same conversation, keep running.
A call also ends when the page closes, stays hidden for 30 seconds, or loses its
connection to RocketClaw, and stops with "Voice call failed: audio connection
lost." when the browser's audio connection fails. There are no idle or no-speech
timers. If the audio connection is not up 20 seconds after OpenAI answers, the
call restarts once on the same microphone with "Voice connection timed out.
Retrying once."; a second timeout stops it. Other failures show a message and are
not retried. A hand-off the agent could not accept shows a notice, and the call
continues.

**Sticky**, the magnet button next to the agent and voice pickers at the top of the
Voice page, keeps a call going through dead zones. It
is off by default, lives only in the tab's memory, and can be turned on before or
during a call. While it is on, a call that loses its connection (the audio
connection fails, the page loses RocketClaw, RocketClaw loses OpenAI, or the audio
is not up 20 seconds after OpenAI answers) starts a new call on the same
conversation and microphone instead of ending. The first try comes 1 second
later, and each failed try doubles the wait, up to 10 seconds; the status under
the orb reads "Reconnecting…" meanwhile. A falling two-note tone marks the loss, a short
tick plays every 5 seconds while reconnecting, and a rising two-note tone marks the
call coming back. Each new call is a fresh OpenAI call that starts from the
conversation's recent messages, so talk never handed off to the agent is not
carried over. Every other ending turns Sticky off: hanging up, the page closing or
staying hidden for 30 seconds, another device taking the call, OpenAI ending it,
and errors a retry cannot fix. Leaving the Voice page turns it off too, and turning
it off while reconnecting stops further tries.

The agent's final reply to a hand-off is spoken once, as written. A reply over
about 990 tokens is not spoken: the voice says it is on the screen, and the Voice
page turns its transcript on and shows the reply there, formatted as in the
conversation. Questions are announced and
answered in the app, never by voice. Voice needs microphone permission and a
secure page: HTTPS, or `http://127.0.0.1` on the RocketClaw machine. See the
[root README](../../../README.md#voice) for phone setup with `web.tsnet`,
credentials, cost, and what OpenAI receives.

## Frontend error and performance tracking

Set `web.sentry` in either `rocketclaw.json` or `femtoclaw.json`:

```json
{
  "web": {
    "sentry": {
      "dsn": "https://PUBLIC_KEY@oORG_ID.ingest.sentry.io/PROJECT_ID",
      "environment": "production",
      "traces_sample_rate": 0.1
    }
  }
}
```

Use the **public DSN** from your Sentry project's Client Keys settings, not an
auth token or a DSN containing a private key. Only these three settings and
the running module version (as the Sentry release, omitted for local
`(devel)` builds) are embedded in the HTML; the rest of the runtime config is
not exposed. Restart
RocketClaw and reload the browser after changing them. No frontend rebuild is
needed for config changes. When both filenames exist, `femtoclaw.json` takes
precedence.

With a DSN, the browser reports uncaught JS/TS errors, unhandled promise
rejections, React rendering errors (caught, uncaught, and recoverable),
`console.error`, and handled request, session-list stream, upload, clipboard,
and query/mutation failures. Cancelled requests are not reported as failures.
Error reports include stack
traces; React reports also include component stacks. Errors are not sampled,
regardless of the performance trace rate. Duplicate reports may be collapsed.

The browser also sends page-load, History API navigation, fetch/XHR,
click-interaction, main-thread stall, and Web Vitals performance data to Sentry.
Stalls are recorded during active traces; this does not measure every freeze or
detect a crashed browser. Browsers with the Long Animation Frames API (Chromium)
report slow frames as `ui.long_animation_frame` with the script that caused them
(`browser.script.invoker`, `code.file.path`, `code.function.name`, and
`browser.script.source_char_position`); frames with no script attached, such as
pure layout or style work, are not reported. Other browsers report
`ui.long_task` without a cause. Browser support determines which metrics
are available. Click tracing uses Sentry's experimental Interactions integration.
Page loads and navigations are named by screen (`/s/:id` for any session).
The two-second background refreshes (`Protocol`, `Identity`, `ListSessions`,
`ListAgents`, and `ListQueue`) are not traced, so they neither keep page loads
and navigations open nor start traces of their own.

Performance sampling defaults to `0.1` (10%). Set `1` to trace every operation
while checking the setup, then lower it to control volume. Rates must be between
`0` and `1`; `0` disables trace sampling, not error reporting. Omit `web.sentry`
or leave `dsn` empty to disable the SDK entirely. `environment` is optional.

No session replay, general console logs, breadcrumbs, user identity, cookies,
headers, request/response bodies, or URL query parameters are automatically
collected. Error messages and `console.error` arguments are collected and can
contain sensitive text; do not put chat contents or secrets in them. The SDK's
data settings do not remove arbitrary sensitive text from error messages.
Collected URLs include paths and can contain conversation IDs.
Click/Web Vitals metadata can include DOM selectors and element labels.
No tracing headers are added to backend requests. Browsers must be able to reach
the DSN's ingest host; any deployment Content Security Policy must allow that
host in `connect-src`.

To check a deployment, temporarily set the sample rate to `1`, reload, navigate
between pages, and click a control. Check the browser Network panel for requests
to Sentry's envelope endpoint, then check the selected project's traces and Web
Vitals. Main-thread work over 50ms appears as a stall when a trace is active.
Blocked ingest requests or an invalid project's client key can prevent delivery;
local config validation does not verify the Sentry account.

### Readable error stack traces

Each frontend build generates source maps and Sentry debug IDs. The maps and
copies of the matching JavaScript are saved under the repository's
`.tmp/sentry-sourcemaps/`. RocketClaw embeds and serves the JavaScript, but not
the source maps. Upload this directory to the same Sentry project before
deploying the built assets:

```sh
SENTRY_ORG=your-org SENTRY_PROJECT=your-project SENTRY_AUTH_TOKEN=your-private-token bun run sentry:upload
```

Run this from `internal/rocketclaw/web` after `bun run build`. Keep the private
token in your build environment or secret store, never in `web.sentry` or browser
code. Upload the maps from the exact build being deployed; the next build replaces
the local map directory. Without an upload, errors still arrive but their stack
traces point to minified code. See [Sentry's source-map upload guide](https://docs.sentry.io/platforms/javascript/guides/react/sourcemaps/uploading/cli/).

To check error delivery, trigger a test error from an app event handler in a test
deployment, then check Sentry Issues for the error and mapped stack trace.
Developer-console exceptions may not trigger browser error monitoring. Errors
before the SDK loads, blocked delivery, and browser or extension crashes cannot
all be captured. Local tests check outgoing envelopes and matching source maps,
not acceptance by a hosted Sentry project.

### Automatic upload on GitHub releases

The `Upload Sentry source maps` workflow runs when you publish a GitHub release,
including a prerelease. Creating a tag or saving a draft release alone does not
trigger it. It checks out the release tag, rebuilds the frontend with Bun 1.4.2,
and uploads the maps only if every rebuilt JavaScript bundle matches the JavaScript
already embedded in that tag. CSS and fonts do not have uploaded source maps and
are not compared. It does not modify the tag, publish binaries, or attach
source maps to the public release.

Configure these once in **Settings → Secrets and variables → Actions**:

- Repository secret `SENTRY_AUTH_TOKEN`: a Sentry organization token permitted
  to upload source maps. Keep it out of runtime config and source control.
- Repository variable `SENTRY_ORG`: your Sentry organization slug.
- Repository variable `SENTRY_PROJECT`: your Sentry project slug, for the same
  project as the browser DSN.

Then publish releases as usual and wait for the workflow to pass before deploying
the release tag. Missing credentials, failed uploads, or mismatched JavaScript fail
the workflow rather than silently skipping the upload. If JavaScript bundles
do not match, use the workflow's pinned Bun version to regenerate and commit
`internal/rocketclaw/internal/web/dist` before cutting the next release. Keep the
workflow's Bun version and the version used for committed frontend builds aligned.
After correcting credentials, rerun the failed workflow from the Actions tab.

## Local checks

Use Bun 1.4.2 or newer. From this directory, run:

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

Build these assets before building `cmd/rocketclaw`, then run that Go executable
with Web automatically listening on `0.0.0.0:3000` (override with `web.listen_address`
in the JSON configuration). Navigation uses the browser History API;
non-API page paths must return `index.html`, including direct `/s/<base64url-id>`
and `/cron` loads. The source lives in `internal/rocketclaw/web/`; no JavaScript server is shipped.
Keep `TMPDIR` inside the repository's `.tmp/` for builds and tests.

The local `go.mod` is a boundary that keeps this JavaScript project and its
`node_modules` dependencies out of repository Go test and lint scans. CLOC also
excludes `node_modules`; frontend source uses the separate JS/TS budget.

The Config page shows the configured Web username and the connected device's
Tailscale user login. RocketClaw runs `tailscale whois --json` on the browser's
connection IP; the `tailscale` executable must be on its PATH and able to reach
the local Tailscale service. Missing or unsuccessful lookups display
"Unavailable". Explicit `web_users` mappings take precedence for admission and
message attribution; unmapped browsers are admitted by their Tailscale login.
New messages use `UserProfile.DisplayName`, falling back to the admitted login
when the display name is missing or blank. Ownership, routing, and Config's
Tailscale display remain login-based. Failed lookups, missing logins, and tagged
devices without a manual mapping are denied. Successful WHOIS results are cached
for five minutes. These IP rules apply to the `web.listen_address` listener. On the
`web.tsnet` listener the Tailscale node identifies each connection itself: it
refuses tagged or unknown devices, never consults `web_users` or the CLI, and the
Config page shows the connection's login.

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

## Tabs

Tabs follow editor semantics.
Sessions and pages (New session, Search, Voice, Cron, Agents, Skills, Settings) open in
one strip, and a location never appears in two tabs. The URL always names the
active tab.

- **Preview tab.** Opening a location that is not already open (Search
  results, links, footer buttons, page commands) shows it in the single preview
  tab, drawn in italics. The next such open replaces it; with no preview tab, the
  new one opens right of the active tab. Double-clicking the tab, typing a draft or
  sending a message in it, or pinning it makes it permanent. A session chosen in the
  palette opens as a permanent tab right of the active tab, leaving the preview tab
  in place, or makes its already open tab permanent. Arrowing through the palette's
  sessions shows each in the preview tab, without adding history entries; a session
  that would open in another space is skipped. The palette's commands act on the
  location it opened at: typing or pressing a command returns there first, and
  dismissing the palette returns there too. A deep link or reload opens its location
  as a permanent tab. Sending the first message from New session turns that tab
  into the new session's tab.
- **Pinned tabs** are browser-only (unlike **Pin session**, which is shared and
  orders search). They stay first, show only their icon on top placement, show a
  pin where the close button would be, and survive Close others, Close to the
  right, and Close all.
- **Context menu.** Right-click a tab, press Shift+F10 or the menu key on a focused
  tab, or use the **…** button on the active tab (for touch) to reach Close,
  Close others, Close to the right, Close all, Pin/Unpin, and Move tabs to
  left/top. Session tabs also offer **Rename session**, which edits the name in
  the tab itself: Enter or clicking away saves, Escape cancels, and a blank name
  shows the last message again. The command palette offers the same commands as
  **Tabs: …** for the active tab.
- **Width and full names.** Top tabs fit their title up to 20rem, then truncate.
  Hovering a tab shows its full title in a tooltip; while it shows, the first
  Escape dismisses it.
- **New session.** Double-clicking empty space in the strip opens New session,
  like the New session button.
- **Closing.** The × button, a middle-click, the menu, a page's close button, or
  Escape on a page closes a tab. Closing the active tab activates its right
  neighbour, or its left one at the end. Closing the last tab opens New session;
  New session alone does not close. Closing with the × button or the menu moves
  keyboard focus to the selected tab.
- **Notifications.** When an open session tab, RocketClaw or remote OpenCode,
  finishes answering, the browser shows a desktop notification with the start
  of its reply. A RocketClaw session's notification is titled by its name, or
  its agent when it has none; an OpenCode session's by its tab title. Clicking
  the notification opens the tab. The tab in view (the active tab while the
  window has focus) does not notify, though another RocketClaw window with the
  same tab open still does. Notifications are on by default: the first click or
  key press asks for the browser's permission, the earliest a browser allows;
  turn them off in the browser's site settings. A finished turn is noticed on the session list's
  poll, so a reply shorter than one poll may not notify, and a hidden window may
  notify late.
- **Reordering.** Drag a tab onto another. With a tab focused, Shift+Arrow moves it.
  A moved tab joins the group of the tab it lands on, or leaves its group when it
  lands on an ungrouped tab.
- **Groups.** Named, colored groups of adjacent tabs, as in Chrome. The tab menu
  offers **Add to new group** (an unnamed group in the next color, with its
  editor open and the name field focused), **Add to group "…"** for each other
  group, and **Remove from group**. A group's header sits before its tabs: a
  colored chip with the name, or a color dot when unnamed. Clicking it collapses
  or expands the group. Right-click, Shift+F10, or the header's **…** button opens
  the editor: name (saved on Enter or leaving the field), nine colors, **New tab
  in group** (moves New session into the group, or opens it there), **Ungroup**,
  and **Close group**, which closes the members by the usual close rules. On the
  left, members sit under the header behind a colored rule; on top, they get a colored underline. A tab opened while
  a grouped tab is active joins that group. Pinned tabs are never grouped:
  pinning a tab removes it from its group. A collapsed group still shows its
  active tab, and activating any hidden tab, including with Cmd/Ctrl+[ and ],
  expands its group. Arrow keys skip hidden tabs. Headers report
  `aria-expanded`.
- **Spaces.** Spaces are shared by everyone: the server keeps one list, oldest
  first, starting with **Main**, and names are unique ignoring case. Every tab,
  pinned or not, belongs to one space, and the strip shows only the current
  space's tabs. The space button at the start of the strip names the current
  space; its menu lists the spaces to switch to and offers **New space**,
  **Rename space**, and **Delete space**, which change the space for everyone.
  A new space takes the first free "Space N" name and is then named in place of
  the button: Enter or clicking away saves, and Escape, a blank name, or a name
  another space has keeps the old name. Switching returns to
  the space's last active tab, or to its first tab. New session is one tab, so
  entering an empty space, opening New session, or closing a space's last tab
  moves that tab into the current space. Opening a location that is open in
  another space switches to that space. The tab menu's **Move to space** submenu
  lists each other space, a moved pinned tab stays pinned, and moving the active tab follows it; the command
  palette offers the same moves as **Tabs: Move to "<space>"**. Moving a tab is
  your own: it moves only in your tabs. Deleting a space moves its tabs to the
  first space; the last space cannot be deleted. A tab
  moved between spaces becomes permanent, so each space keeps at most one preview
  tab. Pinning and unpinning keep a tab in its space. Close others, Close to the
  right, Close all, Cmd/Ctrl+[ and ], and the preview tab act only within the
  current space. Screen readers hear each unpinned tab's group, if any, and space
  as its description, such as “Group Work, Space: Main”.
- **Agents and crons in spaces.** An agent or cron whose frontmatter sets
  `space:` belongs to the space of that name, ignoring case; a cron without one
  takes its agent's. The Cron page and Run cron palette hide crons, and their
  runs, that belong to another space.
- **Birth space.** A session is born in its agent's space; a Run cron session, a
  cron's report thread, or a chat continuing a cron run in its cron's space, else
  the cron agent's; any other Web session in the space you were in. A space named
  this way that does not exist yet is created with the session. A session opens
  in the space it was born in, and after its first message a new session moves
  there and takes you along. When you have moved a session's tab elsewhere, its tab
  menu offers **Restore to "<space>"**. Sessions from before birth spaces, and
  sessions started outside the Web other than cron report threads, have none and
  open in the current space.
- **Agent picker.** New session offers every agent. Once a session's agent has a
  space, its picker and `$agent` offer only agents with that space, ignoring case;
  when that leaves none the Slack channel or cron allows, they offer all of those.
- **Groups in spaces.** A group lives in one space. The tab menu offers only the
  current space's groups, and moving a tab to another space takes it out of its group.
  The group editor adds **Move group to "<space>"** for each other space: it
  moves every member and keeps the group's name, color, and collapsed state, and
  moving the active tab's group follows it. Deleting a space moves its groups,
  intact, to the first space. **New tab in group** brings New session into the
  group's space as well as the group.
- **Running turns** make the tab's title shimmer with a moving gradient, so the
  title stays in place. A pinned top-strip tab, which has no title, shimmers its
  icon instead. With reduced motion the title or icon shows a still gradient
  that fades toward its end. Screen readers hear “Turn running” in the tab's
  description.
- **Background work** that can wake an idle session swaps the tab's chat icon for an
  hourglass: a running Background Job, its own or that of a hidden run reporting to it, or a
  remote OpenCode session's running child session (a background subagent) or shell command.
  A running turn keeps the chat icon. Screen readers hear “Background work running”.
- **Keyboard.** The strip is a `tablist` with one Tab stop. Arrow keys (Left/Right
  or Up/Down), Home, and End move focus; Enter or Space activates. Anywhere in
  the app, Cmd/Ctrl+[ and Cmd/Ctrl+] activate the previous and next tab, wrapping
  at the ends. This replaces the browser's Back/Forward for those keys.
- **Placement.** Tabs sit on top by default or in a 14rem sidebar on the left. Switch
  from **Settings → Appearance → Tabs**, the context menu, or the palette. Below the `md` breakpoint, the left
  sidebar is a drawer: the **Show sidebar** button at the top left or a swipe right opens it, and picking a tab,
  tapping outside, Escape, or a swipe left closes it. A swipe that starts on a text field, a slider, or something
  that scrolls sideways is left to that element. The top strip scrolls horizontally; a mouse wheel scrolls it sideways.
- **Left sidebar.** Drag its right edge, or focus the edge and press Left/Right, to
  resize it; the width is kept in this browser. The footer's **Hide sidebar** button
  or Cmd/Ctrl+B hides and shows it until the next reload.
- **Storage.** The server keeps each person's tabs, so they follow you to every
  device: open tabs, their order, pins, the preview flag, each tab's group and
  space, the groups' names, colors, and collapsed state, the current space, each
  space's last active tab, and the placement. Each change saves the whole state,
  one save at a time. A save made from tabs older than another window's save is
  refused; the window then merges in that newer save, keeping what it opened,
  closed, or changed itself, and saves again, so neither window's changes are
  lost. Saved tabs that can no longer be read count as none and are replaced by
  the next save. Groups with no tabs are dropped, tabs whose space is gone join
  the first space keeping their group, and tabs whose group is gone leave it. A
  window reloads them when it becomes visible or regains focus: it takes in
  changes made elsewhere, merged with its own unsaved ones, but stays on its own
  tab, unless that tab was closed elsewhere. The address is the active tab, so a
  reload keeps it, and opening any URL, including Home (`/`), activates its tab or adds one. Tabs once kept in
  `localStorage` are not carried over. Titles and running state come live from
  the session list; a session missing from it shows its ID.

Tradeoffs: preview replacement keeps the strip short without manual closing, but
an item opened once and left unpromoted vanishes on the next open, which can
surprise people. Unlike browser-style tabs there is no
per-tab Back/Forward or reopen-closed-tab. Unlike grouped tabs there are no
agent/age lines. Spaces hide every other space's tabs, which keeps one task in
focus but means a tab in another space is reached through the space menu, the
palette, or a link rather than at a glance. Unlike warm tabs, switching sessions remounts the
transcript. In exchange, the strip stays compact, and the menu, pins, and drag
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
desktop uses 32px targets and 4px gaps. Icons are 24px in both layouts.
The centered bar has New session, Search sessions and commands, Voice, and Copy
page link.
Cmd/Ctrl+Alt+N opens a new session. The magnifying glass beside New session opens
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
a long title shows the title from just before it, highlighted. Typed text ends the list with **Search everywhere
for '…'**, which opens the Search page with that text, also when it is already
open. Each dialog opening clears text, pills, and result selection and focuses
the input.
Once text is typed, matching commands appear above the conversations; an empty
query, or one with a filter such as `tag:customer` or `is:pinned`, lists
conversations only. A bare key such as `cron:` is not a filter. Commands match
case-insensitively: a label equal to the text comes first, then labels containing
it, then, once three or more characters are typed, labels containing them in order
with gaps. Within each group the command order below applies. Enter runs the
highlighted command at once when its label contains the text or it was picked
with the arrow keys; otherwise Enter opens the first conversation, as above, and
plain text that matches no conversation opens Search everywhere.
Clicking a command always runs it.

Cmd/Ctrl+P matches plain text against titles in the browser as you type, with no
request. A query with a filter, even a bare key such as `principal:`, goes to the
server, which applies only the filters; the palette matches the rest against the
titles of the conversations that pass, the same way as plain text. A failed
filtered search lists no conversations.
The Search page sends every query to the server. Queries are sent once typing
pauses for 250 ms (the Search page also at least once a second while typing),
Enter sends one at once, and a newer query cancels the older request.
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
query. Choosing an agent replaces earlier agent filters; choosing a room keeps
earlier rooms. Multiple room filters match either room.
A remote OpenCode session's agent is the one it has selected, so `agent:build`
also finds those. `server:box` keeps the remote sessions of the configured
OpenCode server `box`, exactly. `model:opus` keeps conversations whose model
contains the value, ignoring case: a remote session's selected model, such as
`anthropic/claude-opus-4-7`, or a RocketClaw chat's agent's configured model.
Conversations whose whole model equals the value come first. Typing `server:`
or `model:` suggests the servers and models in use, and typing `agent:` also
suggests remote sessions' agents.
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
Other repeated filters use AND: `tag:customer tag:triage` requires both tags. Unknown
tags match nothing. Bare `tag:`, unclosed quotes, and invalid quoted values stay
ordinary search text. A filter-only query lists every conversation it keeps;
`tag:customer outage` keeps message hits only from tagged conversations.
Filters constrain both row and message matches. The session-search dialog lists each conversation
once, pinned first, retaining recent-first order within each group.
`sort:newest` or `sort:oldest` instead orders results by last activity on every
search surface, overriding pinned-first and message-hit grouping; rows without
activity come last, equal times order by conversation ID, and the last `sort:` wins.
Typing `is:` suggests `pinned`, `forked`, and `cron`; typing `sort:` suggests
`newest` and `oldest`. While typing, a last word that is an unfinished filter
(a bare `tag:`, `cron:`, `agent:`, `principal:`, `room:`, `server:`, or `model:`, a partial `is:` or `sort:` term
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
and **Help: Keyboard Shortcuts**, plus **Tabs: Toggle sidebar** while Cmd/Ctrl+B
works (tabs in the desktop left sidebar). In both palettes a command bound to a
key shows it on the right of its row, OpenCode style: **⌘ Option N** on Mac or
**Ctrl Alt N** elsewhere for **Sessions: New**, **⌘ [** and **⌘ ]** for the tab
commands, and **⌘ B** for the sidebar toggle. Unbound commands have no shortcut
hint. **Help: Keyboard Shortcuts** opens the read-only `/shortcuts` page, which
lists every shortcut from the same list: the palette keys, new session, tab
stepping, the sidebar toggle, Escape on a page tab, and the message box's steer
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
**Name session**, **Pin/Unpin**, **Fork session**,
**Handoff session**, **Choose agent**, and **Stop turn** while running.
Fork, handoff, rename, and agent selection open their controls directly.
**Command: Start goal ($goal)**, **Command: Run workflow ($workflow)**,
**Command: Invoke skill ($skill)**, **Command: Enqueue work ($enqueue)**,
**Command: Stash work ($stash)**, and **Command: Steer turn ($steer)**
prefix the corresponding `$command` to the current draft and focus the composer.
Existing draft text and attachments are kept; nothing is sent until you submit.
In web, `$enqueue <text>` queues automatic later work, `$stash <text>` holds work
until explicitly sent, and `$steer <text>` sends now or guides the running turn.
Only the outer delivery command is removed; inner commands and text are preserved.
`$workflow <name> [args]` runs a saved workflow as its own turn: right away when
idle, after the active turn when steered, and as later work when queued. Bare
`$workflow` privately lists the saved workflows, and typing `$workflow ` in the
composer suggests them by name. `$goal <objective>` starts a goal
loop on the conversation, with `maxTurns:` and `checkScript:` options; its first turn runs right away when idle, otherwise after the active turn.
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

The composer is a [Prompt Input](https://ui.shadcn.com/docs/ai-elements/base/prompt-input):
an Input Group with pending files above the text and its controls below. Use its
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
Enter and **Send** queue later work while a turn is running. The **Steer** button
immediately left of Send guides the active response; Cmd/Ctrl+Enter does the same.
Steers wait in **Waiting to steer**, separate from both chat and the later-work
queue. A persisted input in a History delta moves that input into chat at the point
it was used, with its attachments. Identical texts remain separate inputs, tracked
by durable input ID. Queue promotion keeps the server queue ID and follows the
same consumption rule. Replacing the active turn includes each consumed steer
without repeating the earlier response.
Use **Stash** to save the draft and its attachments without sending, whether the
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
Forked sessions show a fork glyph in session rows. Use **Open original conversation**
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
without starting a turn. Start new session sends it as the first turn.
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
The theme toggle floats in the upper-right corner of the page and switches light, dark, and system. Config has a select for the color theme. The choices are Neutral, Harbor, Grove, Ember, Violet, Rose, Sand, Lagoon, Slate, Copper, Ink, Signal, Go - Playground, and Go - Sources. Ink and Signal are high contrast. The Go themes adapt Mike Gleason jr Couturier's light themes, with local dark variations. The choice is stored in the browser, not on the server. See [theme sources and licenses](../../../THEMES.md) for attribution and reuse terms.
Config's Timeline section has the **Timeline detail** card, following OpenCode v2. A
five-stop slider picks Messages only, Quiet, Compact, Detailed, or Everything, and an
Advanced table sets visibility, Group, and Collapse per category; settings that match
no level read as Custom. Everything expands every tool call, thinking trace, and notice,
and every category has a Collapse switch. A browser that never chose shows Compact. The setting is saved
in this browser under `timeline-detail`, applies to every open timeline at once,
including other tabs, and is not sent to the server.
The down-arrow appears above the chat composer when away from the latest message.
It scrolls to the latest message and resumes following live replies. Reading earlier
messages keeps automatic scrolling paused.

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
the name and the preview remain searchable. Clear the name to restore the preview.
Pins and names are shared with everyone who can see the session and persist
across reloads and devices.

Cron runs are grouped beneath their definition, with links to their chat.
Search above the grid filters both the grid and the groups by case-insensitive
text matching across definitions, metadata, and run times, keeping matching groups together.
Each cron group starts collapsed, with a separate, initially collapsed Definition
disclosure containing labeled schedule, agent, channel, and source fields above
its body. Long field values wrap instead of truncating. The grid distinguishes
recorded runs (filled cyan) from expected runs (outlined amber), with time labels.
The grid shows the past 12 hours and next 24 hours, including runs whose definition
is gone. Marker tooltips open on hover, keyboard focus, or tap; Escape dismisses them.
Runs without a delivered chat still appear, with their timestamp and “No delivered
chat” with **Open chat** to inspect their recorded reasoning and tool activity.
Click a recorded run marker or the bar (for the latest run) to preview that run;
every recorded run offers **Open chat**. Undelivered runs create or reuse a writable
Web chat with the run's synced history, without rerunning the job or creating a Slack
thread. Repeated opens return the same chat. Its agent choices follow the current
cron definition's channel and configured channel agents; if those choices cannot
be computed, all loaded agents are available. The first allowed loaded agent is
selected when the chat is created. Delivered runs keep opening their existing chat.
Previews include only history from the selected run. Removed definitions keep
their recorded runs visible. A play icon beside each cron name opens an in-app confirmation dialog
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
on its own when its category is hidden, and a call that delivers files always appears
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
too, without Open. Calls running in the background stay visible at every Timeline
detail level. Background Jobs and Completion Notes are Trace lines too: a running
job spins, and a failed or killed one shows in red. Each Completion Note is one
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
A tool row whose call delegated work (an `auto` permission review, a guardrail
check, or a Task subagent) shows **Open delegation**. A `task` row opens its
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

The saved-list tests use real Chromium and IndexedDB. Enable them by supplying
the installed Playwright module and Chromium executable after `bun run build`:

```sh
export ROCKETCLAW_PLAYWRIGHT_MODULE='/absolute/path/to/playwright-core/index.mjs'
export ROCKETCLAW_CHROMIUM='/absolute/path/to/chromium'
bun test src/session-list.browser.test.ts
```

They check a 17 MiB snapshot across reload, user/protocol isolation, transaction
abort after request success, deletion winning over a delayed save, delayed
hydration, owner switches, and actual-App restore/merge/identity
behavior, including a connection cut after terminal metadata and protocol changes.

## Attachment HTTP contract

The Go HTTP handler serves attachments alongside the protobuf JSON API and
`/stream`. TanStack Query owns ordinary query caches; session enumeration is a
finite SSE fetch at `GET /api/ListSessions`. A terminal snapshot is not transport
completion: only `event: complete` followed by successful EOF permits a saved
snapshot. Errors, cancellation, and EOF without that event fail enumeration.
`POST /api/Protocol` returns `{ "protoSha256": "…" }`; polling reloads the page
when the protocol changes. `GET /stream?id=<url-encoded-raw-id>` delivers only
content-free conversation-change hints via EventSource. Each hint triggers a History
delta read from the last applied revision; opening or reconnecting also catches up.

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

Slack installation requirements, including channel rename subscriptions, are in
the [RocketClaw cheatsheet](../cmd/rocketclaw/CHEATSHEET.md#slack-channel-rename-subscriptions).
