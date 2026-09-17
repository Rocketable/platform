# RocketClaw Web

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

The build includes TypeScript checks. The entry, live and sidebar HTTP integration
tests use `ROCKETCLAW_TEST_HTTP_URL`, supplied by the Go test harness running
`rpc.NewHTTPHandler`. They skip when that URL is absent. Use the isolated
PostgreSQL and browser-test instructions in the transport README to exercise
those paths. Keep temporary test artifacts under the repository's `.tmp/`.

The sidebar restores the last complete list only after live identity confirms the
Go-mapped username, keys native IndexedDB by owner and protocol, and keeps that
snapshot until a refresh is successfully exhausted with complete summaries.
Each row previews the latest nonempty user or assistant message, including delivered
assistant reports. A small spinner in the metadata line indicates a running turn
without moving the row; saved snapshots do not restore an old running indicator.
Routine background refreshes are silent; stale rows and missing summaries still
show their status (`Stale` or `loading...`).
Failed, partial or cancelled streams do not replace the snapshot. History
deletion clears preview text without removing the row. Composer choices and
sends stay on the agent query and do not wait for sidebar refresh.
Click or tap the handle to hide or show the bottom navigation, or swipe the handle
down to hide it and up to show it. On mobile it floats over a slim 16px strip while
retaining a 44px touch target. The handle stays
visible when the controls are hidden and supports Enter/Space when focused.
Touch devices and narrow screens use 48px navigation targets and 8px gaps;
desktop uses 32px targets and 4px gaps. Icons are 24px in both layouts.
Desktop keeps all seven controls in the bar. On mobile, the Sessions toggle sits
at the top-left opposite the theme toggle, leaving six controls centered below.
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
queue. A server consumption event moves that input into chat at the point it was
used, with its attachments. Identical texts remain separate inputs, tracked by
message ID. Queue promotion keeps the server queue ID and follows the same
consumption rule. Cumulative streamed replies continue after the consumed steer
without repeating the earlier response.
On reconnect, saved history replaces completed chat; pending steers remain separate.
An active local transcript keeps its live IDs and stream segments. History confirms
stored attachments by file ID, replacing local previews with download URLs. A history
request that overlaps newer live changes cannot overwrite those changes. If a
direct steer's consumption event was missed, its own successful Prompt completion
reloads saved history and clears that pending ID. History has no durable message
IDs, so the browser does not guess consumption by matching text.
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
The sidebar starts at the top without a title bar. Its fold/unfold toggle stays at the bottom-left
when hiding or reopening it, retaining search and filters during navigation.
Settled chats live on the **Settled** tab (`/settled`) rather than in the default
sidebar. Use **Unsettle** to return a chat to the sidebar. Adding `is:settled` to
sidebar search includes settled chats alongside matching active chats; the token
is not matched as text, and agent and room filters still apply. The Settled page
has the same search and filters, limited to settled chats.
Click the selected Settled, Cron, Agents, Skills, or Config tab again to return
to the chat you left, even after switching between pages. Without a previous
chat, it returns Home. New chat and these page buttons form a centered group in
the footer, available even when the sidebar is closed.
The theme toggle floats in the upper-right corner of the page.
The down-arrow appears above the chat composer when away from the latest message.
It scrolls to the latest message and resumes following live replies. Reading earlier
messages keeps automatic scrolling paused.

Pin a session from its sidebar row or the pin button in the composer. Pinned sessions
sort first, keep their normal recent-first order within that group, and do not
automatically settle. You can still settle them manually. Search `is:pinned` to
find only pinned sessions, including settled ones; text, agent, and room filters
still apply. On the Settled page, results remain limited to settled sessions.
Sidebar text uses the full row width. Rename, Pin, and Settle overlay the text on hover or
keyboard focus; touch screens show the overlay directly.
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
Each turn has an inline **Thinking** disclosure, expanded by default, containing
reasoning summaries and tool traces. Replies and successful verbatim-delivery
reports appear as normal messages outside that disclosure. The chat has no
database-entry inspection panel.
Each tool call has one labeled, initially expanded disclosure containing its
arguments and matching result. Loaded skill instructions fold with their skill
call. Long results have a collapse control at the bottom as well as the header.
The transcript has a visible scrollbar and a side rail with one jump marker per
turn. Hovering or focusing the rail opens a scrollable box of message previews;
clicking a preview or its marker jumps to that turn. The box overlays the chat
without moving it, and scrolling the box does not scroll the transcript.
Each marker is labeled with its prompt; selecting it pauses automatic
following while you read earlier turns.
The shared app layout keeps the list and filters in memory during navigation.
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
when the protocol changes. `GET /stream?id=<url-encoded-raw-id>` delivers live
transcript events via EventSource.

- `POST /api/UploadAttachment?conversationId=<visible-id>&name=<filename>` takes the raw
  file as its body and returns JSON attachment metadata. Query values use normal
  URL encoding, not the session route's base64url encoding.
- `GET /api/DownloadAttachment?conversationId=<visible-id>&id=<attachment-id>` streams the
  file. Safe raster MIME types use inline disposition; other types download.
  Add `&download=1` to force a download for an image too.
- `POST /api/Prompt` accepts `attachmentIds` in selection order alongside unchanged
  `id`, `text`, and `delivery`. The backend adds attachment references to the text.

All JSON RPCs use PascalCase method names, camelCase protobuf fields, and
protobuf response envelopes (for example `{ "messages": [] }` for History).
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
