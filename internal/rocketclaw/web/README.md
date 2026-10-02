# RocketClaw Web

Assistant replies show a smaller, muted footer with known
`agent (provider/model#effort)` details; `#effort` is omitted when absent.
On desktop, hover a message to see its footer and Copy button. On touch
screens, tap a message instead. User messages show Copy and, when recorded, a
message-header info button, but no agent or model label.
Copy puts the message text on the clipboard.
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
Reasoning groups have no footer or Copy action.
When a session contains sandboxed messages, a slim button group above the
composer highlights both `sandboxed` and `canonical` by default. Each button
independently toggles its origin on or off, so both, either, or neither can be shown.
Messages without a recorded origin appear only when both are selected.
Filtering does not change saved history or routing.

The frontend is a client-side React SPA. Bun builds static assets directly into
`../internal/web/dist/`: `index.html`, hashed JavaScript and
CSS, and a self-hosted Inter font. `cmd/rocketclaw` embeds and serves that directory
alongside the Go HTTP API. Production needs no Bun, Node, or Next server.

See [Web conversation transport](../frontend/rpc/README.md)
for startup commands, browser IP mappings and the transport contract. The Go
handler takes identity from the browser connection; forwarded headers cannot
select a user. Serve it directly to browsers rather than through another HTTP
reverse proxy.

## Local checks

Use Bun 1.4.0 or newer. From this directory, run:

```sh
bun install --frozen-lockfile
bun run lint
bunx tsc --noEmit
bun run build
bun test
```

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
“Unavailable”. This is display-only: configured IP-to-user mappings still
control access, and a Tailscale identity does not grant access on its own.

Oxlint runs `@shadcn/lint` with `no-restyle` enabled, including inside the UI
components. Use component variants for styling and `className` for layout;
the configuration is in `.oxlintrc.json`.

The UI uses the shadcn `b0` preset with Base Nova components, configured in
`components.json`. Message rows compose `Message` and `Bubble`; `MessageScroller`
owns streaming follow and turn jumps. The turn rail uses its `scrollToMessage`
API, and sending a message resumes following through `scrollToEnd`.

The build includes TypeScript checks. The entry, sidebar and public-progress HTTP integration
tests fail unless their Go test servers are running. `go test` in
`frontend/rpc` runs each of them against its own server. CI starts those servers
with `ROCKETCLAW_WEB_TEST_SERVER_DIR` before `bun test`; see
`.github/workflows/test.yml`. Use the isolated
PostgreSQL and browser-test instructions in the transport README to exercise
those paths. Keep temporary test artifacts under the repository's `.tmp/`.

The sidebar restores the last complete list only after live identity confirms the
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
sends stay on the agent query and do not wait for sidebar refresh.
Click or tap the handle to hide or show the bottom navigation, or swipe the handle
down to hide it and up to show it. On mobile it floats over a slim 16px strip while
retaining a 44px touch target. The handle stays
visible when the controls are hidden and supports Enter/Space when focused.
Touch devices and narrow screens use 48px navigation targets and 8px gaps;
desktop uses 32px targets and 4px gaps. Icons are 24px in both layouts.
The center bar has New session, Search sessions, and Open command palette. The
separate desktop sidebar toggle and collapse handle remain. Cmd/Ctrl+B hides and shows the sidebar.
Cmd/Ctrl+Alt+N opens a new session. The magnifying glass beside New session opens
the Search page. Each search has its own editable tab, saved in this browser for
the confirmed user. Click another tab to switch searches; click the selected tab
to edit its name in place. Enter or clicking away saves; Escape cancels.
Custom names stay unchanged when the query is edited; a blank name
restores the query label. Results use a compact Search Editor-style list, grouped by
conversation. The match count sits in parentheses beside each title, followed by
highlighted, clickable matching excerpts. Message matches
jump to the recorded user or assistant message, including after a reload;
name and origin matches open the conversation. Message groups come before
metadata-only matches, with pinned conversations first within each group.
An empty search asks for a query instead of listing conversations. Closing a tab
removes that search; closing the last returns to the last message visible in any chat, or Home
if none was seen. Opening Search again starts an empty tab. Cmd/Ctrl+P still opens
the session-search dialog, including active and settled chats. Each dialog opening
clears text, pills, and result selection and focuses the input.
Search matches names, titles, previews, agents, session labels, origin card fields,
and original MCP metadata keys and values, case-insensitively. Type `agent:` or
`room:` to choose a removable filter pill by click, arrows, Tab, or Enter.
Suggestion keys take priority over results; Escape dismisses suggestions first,
then closes the dialog. Both Cmd/Ctrl+P and the Search page accept exact,
case-sensitive tag filters: `tag:customer`, or `tag:"Needs review"` for a name
containing spaces. Quoted values use JSON escaping, such as `tag:"say \"hello\""`.
Repeated filters use AND: `tag:customer tag:triage` requires both tags. Unknown
tags match nothing. Bare `tag:`, unclosed quotes, and invalid quoted values stay
ordinary search text. A tag-only query loads no transcript; `tag:customer outage`
sends only `outage` to transcript search and keeps hits from tagged sessions.
Agent and room selections preserve tag filters, and saved queries keep their raw
syntax without a migration. Agent, room, tag, `is:pinned`, and `is:forked` filters constrain
both row and origin matches. The session-search dialog lists each conversation
once, pinned first, retaining recent-first order within each group.
Origin data loads only when free text remains: empty input, status tokens, pills,
and unfinished `agent:`/`room:` suggestions do not fetch histories. Ordinary
sidebar loading stays independent. Incomplete enumeration or origin lookups show
loading feedback rather than a definitive empty result. Failed origin lookups
show an error while usable row matches remain selectable.
The command button and Cmd/Ctrl+Shift+P open the command palette. It always offers
**Sessions: New**, **Sessions: Search**, **Sessions: List Settled**, **Cron: Dashboard**,
**Cron: Run**, **List Agents**, **List Skills**, **Settings**, and **Hide Sidebar / Show Sidebar**.
Timeline detail levels are also available, from `Timeline: Messages only` through
`Timeline: Everything`.
Like VS Code, selected commands appear first, newest first;
unused commands follow alphabetically, without section dividers. History stays in
this browser across reloads, stores command IDs rather than session IDs or draft
text, and records only selections—not searches or dismissals. Search still filters
the list, and unavailable session commands stay hidden even when recently used.
Rows keep compact spacing and touch-sized targets on mobile.
For the current session it also offers **Open original conversation** (when forked),
**Name session**, **Pin/Unpin**, **Snooze session**, **Settle/Unsettle**, **Fork session**,
**Handoff session**, **Choose agent**, and **Stop turn** while running.
The sidebar and palette use the same session-action definitions. Fork, handoff,
rename, and agent selection open their controls directly.
**Command: Start goal ($goal)**, **Command: Run workflow ($workflow)**,
**Command: Invoke skill ($skill)**, **Command: Enqueue work ($enqueue)**,
**Command: Stash work ($stash)**, and **Command: Steer turn ($steer)**
prefix the corresponding `$command` to the current draft and focus the composer.
Existing draft text and attachments are kept; nothing is sent until you submit.
In web, `$enqueue <text>` queues automatic later work, `$stash <text>` holds work
until explicitly sent, and `$steer <text>` sends now or guides the running turn.
Only the outer delivery command is removed; inner commands and text are preserved.
The Stash button and shortcut still hold the entire draft literally, including
any `$command` text. Slack's command language is unchanged.
Run cron opens a new session immediately. The job still runs privately, and the chat fills in when it finishes.
A chat that began as a cron run or an external MCP call starts with a collapsed origin card. Use show more to see its details; the card scrolls with the messages. An ordinary chat has no origin card.
On mobile, the Sessions toggle sits
at the top-left opposite the theme toggle, leaving three controls centered below.
On titled pages, the mobile Sessions button shares the title's header row so
they cannot overlap. In chat, it remains a floating corner button.
Swipe right from the middle of the main content to open the mobile sidebar, or
left from inside the sidebar to close it. Vertical scrolling and text-field
gestures do not trigger the sidebar. Rename and Pin remain available in sidebar
rows; their composer buttons appear only on desktop.
Safe-area padding is preserved. On very narrow screens, navigation buttons scroll
horizontally instead of overlapping.

Use the agent selector in the composer to change agents. New Web threads select
`main` by default; existing threads retain their agent.
New chat opens a fresh Home composer and creates a session only on the first send.
It clears the draft and resets the agent to `main`; returning from other pages
preserves the current draft and selected agent.
Select text in the chat and click **Quote** to append a Markdown blockquote to
the current draft. The composer adds a blank line after the quote and places the
cursor there for your comment. The browser's right-click menu remains available.
Use **+** to choose files, or drop files onto the composer. Pending files stay in
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
Failed Stash keeps the draft and files for retry. If Pop shows an error, check
the refreshed queue: the message may already be queued if starting work failed.
Live updates and refresh read the same persisted transcript. A chat opens with
its newest 50 turns. Scrolling near the top loads the 50 before them without moving
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
slowest sibling. Their rows keep the original call order. Private child diagnostics,
review reasons, and provisional tool arguments are not public progress.
Final text replaces partial text exactly, including when the final text is empty.
Stopped or failed turns retain their progress without becoming resumable work.
Progress does not create command targets or move waiting steers into the model early.
Slack still receives one in-progress placeholder and the final answer/attachments,
not intermediate Web content.
Steer is enabled while a
response is running and the draft has content. Failed uploads or
sends keep the draft and files for retry. Drafts remain in memory when switching
chats or pages; **New session** resets the Home draft. Reloading closes these
in-memory drafts. Sending is locked through upload and dispatch, preventing repeated
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
Forked sessions show a fork glyph in the sidebar. Use **Open original conversation**
on the row to return to the immediate source, or search `is:forked` to find forks.
Sidebar rows show the title, Slack channel when available, agent name and active
tags, fork/running indicators, and relative time. Metadata text starts at the row's
left edge, without a spinner indent. Tags render as plain text; the metadata title
retains long labels. Active and settled rows update even when only tags change.
Owner/protocol-scoped snapshots retain tags; older cached rows without tags stay
usable. Labels follow the existing two-second refresh cycle and do not change
ordering, settlement, snooze, or incomplete-search feedback.
Settled chats, session search, and handoff destinations also show status.
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
The handoff includes the source session ID and points to `rocketclaw_get_session`
for reading more details from that session.
After choosing a destination, **Copy Handoff** also copies it without stashing.
The handoff panel keeps **Preview**, **Copy**, and **Stash** actions visible.
**Preview** opens the full document; selecting a destination shows its conversation
below the panel and hides the composer until the handoff is closed.
The sidebar starts at the top without a title bar or search input. Its fold/unfold
toggle stays at the bottom-left when hiding or reopening it.
Conversations without stored history stay out of the sidebar until their first
entry arrives. Clearing all stored history hides the conversation again without
deleting its record or its Slack messages.
Settled chats live on the **Settled** tab (`/settled`) rather than in the default
sidebar. Use **Unsettle** to return a chat to the sidebar. Global search always
includes settled chats; `is:settled` is accepted as an include token, not literal
text or a settled-only filter. The Settled page retains its own local row search
and agent, room, and pinned filters, limited to settled chats.
Use the clock button in a chat or **Session actions** in a sidebar row to
**Snooze** until a chosen local date and time. Snoozed chats appear under
**Settled** with their return time.
New messages bring them back early, and **Unsettle** ends snooze immediately.
At the chosen time, the chat returns on the next sidebar refresh and gets a fresh
inactivity window, even if its last message is old. Opening a hidden chat does
not unsettle it. Choosing **Settle** cancels a pending snooze.
Click the selected Settled, Cron, Agents, Skills, or Config tab again to return
to the chat you left, even after switching between pages. Without a previous
chat, it returns Home. On desktop, the page header's close button does the same,
and Escape does too unless a dialog or tooltip is open. New chat, search, and these page
buttons form a centered group in the footer, available even when the sidebar is
closed.
The theme toggle floats in the upper-right corner of the page and switches light, dark, and system. Config has a select for the color theme. The choices are Neutral, Harbor, Grove, Ember, Violet, Rose, Sand, Lagoon, Slate, Copper, Ink, Signal, Go - Playground, and Go - Sources. Ink and Signal are high contrast. The Go themes adapt Mike Gleason jr Couturier's light themes, with local dark variations. The choice is stored in the browser, not on the server. See [theme sources and licenses](../../../THEMES.md) for attribution and reuse terms.
Config's Timeline section has the **Timeline detail** card, following OpenCode v2. A
five-stop slider picks Messages only, Quiet, Compact, Detailed, or Everything, and an
Advanced table sets visibility, Group, and Collapse per category; settings that match
no level read as Custom. A browser that never chose shows Compact. The setting is saved
in this browser under `timeline-detail`, applies to every open timeline at once,
including other tabs, and is not sent to the server.
The down-arrow appears above the chat composer when away from the latest message.
It scrolls to the latest message and resumes following live replies. Reading earlier
messages keeps automatic scrolling paused.

Pin a session from its sidebar row or the pin button in the composer. Pinned sessions
sort first, keep their normal recent-first order within that group, and do not
automatically settle. You can still settle them manually. Search `is:pinned` to
find only pinned sessions, including settled ones; text, agent, and room filters
still apply. On the Settled page, results remain limited to settled sessions.
Sidebar text uses the full row width. A **Settle** button and **Session actions**
menu appear on hover or keyboard focus (and stay visible on touch screens).
Settle a session in one click; open the menu for Rename, Snooze, and Pin.
On the Settled page, the menu also offers Unsettle.
The session age stays visible beside the agent; hover it for the exact
last-update date and time in your local time zone.
Use the rename icon beside the pin in the chat to set an optional name. Chat action
buttons have descriptive tooltips. Add files sits at the far left, before the agent
selector. On mobile, these and the session actions sit above Steer and Send. The name
replaces the sidebar's last-message preview without changing the messages; both
the name and the preview remain searchable. Clear the name to restore the preview.
Pins and names are shared with everyone who can see the session and persist
across reloads and devices.

Backend setting `web.auto_settle_after` defaults to `168h` and accepts Go
`time.ParseDuration` syntax (for example, `24h` or `1h30m`, not `7d`). Inactivity
starts at the latest stored chat entry of any role or kind. Running and pinned chats are
excluded from auto-settling. New entries reopen settled chats, including those
settled manually. Manually choosing **Unsettle** starts a fresh inactivity window
without changing message timestamps. Settling is recalculated on each list refresh;
empty chats and chats whose summaries are still loading are not auto-settled.
The Config page's Web section shows the effective `web.auto_settle_after` in
normalized Go duration syntax, including the default (`168h0m0s`, seven days).
Set this in either `rocketclaw.json` or `femtoclaw.json`:

```json
{
  "web": { "auto_settle_after": "168h" }
}
```

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
row. Collapse decides whether Execute calls and reasoning start open. Opening or
closing an item survives live updates of the same turn. Replies always appear. A
failed call (a result starting with `tool call failed`, `denied` or `aborted`) appears
on its own when its category is hidden, and a call that delivers files always appears
on its own. While a turn runs, **Working…** shows at every level. The chat has no
database-entry inspection panel.
A tool row whose call delegated work (an `auto` permission review, a guardrail
check, or a Task subagent) shows **Open delegation**. It opens that Delegation
History read-only beside the chat on wide screens and full screen on narrow
ones, with breadcrumbs for nested delegations. The panel lives in the
`delegation` query parameter, so Back, Forward, and reload keep it. A link at the
end of each transcript goes back up one level. Only finished delegations are
saved. On wide screens the session sidebar and the delegation panel can be
resized by dragging their inner edge, and the width is remembered.
Each tool call has one labeled disclosure containing its arguments and matching
result. Loaded skill instructions fold with their skill call. Long results have a
collapse control at the bottom as well as the header. Cron run previews and the
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
The browser suite also exercises picker/remove/drop, draft retention, failed-send
retry, attachment-only sends and live replies, inline images, downloads, and
queue versus steer behavior.

Slack installation requirements, including channel rename subscriptions, are in
the [RocketClaw cheatsheet](../cmd/rocketclaw/CHEATSHEET.md#slack-channel-rename-subscriptions).
