# Web conversation transport

This bounded transport implements `Protocol`, `ListSessionEntries`,
`LoadSessionEntries`, `DeleteSessionEntries`, `ListSessions`, `History`, `Prompt`, and `Join` from
`internal/rocketclaw/web/proto/web.proto`. Steer Prompt waits for Backend RunTurn completion, using the
authenticated username. Bare `$stop` runs cancel through RunTurn and waits for
that interrupted turn. Queue Prompt stashes waiting work through existing
Backend queue operations and returns without waiting for that turn. `ListQueue`, `SteerQueueItem`, `RemoveQueueItem`, and `ReorderQueue` use
existing Backend queue operations; promotion keeps the queued item's original
principal and message ID. `ListQueue.delivery` distinguishes later work from pending
steers. Join sends a user event with `message_id` when the backend activates queued
work or drains a steer into the current turn. Direct steers retain the client's
`Prompt.message_id`; queued messages use their server-assigned queue IDs. These
events move waiting inputs into chat in consumption order without matching text.
Dropped items never run. Reorder writes persisted enqueue positions. Join streams only live
events for the requested conversation; it does not replay stored history or
return output through Prompt's private response field. Live events carry Backend
turn IDs so incremental answers cannot replace a preceding turn. History returns
the stored turn in order, including developer messages, thinking summaries, tool
calls, tool results, and user/assistant text. Encrypted reasoning bodies stay
stored and are not sent. History retains tool call IDs and names so Web can
group each call with its result.
History and sidebar previews
share display text with one canonical Web prompt envelope removed from user
messages; stored replay, principal framing, and assistant text remain unchanged.
Previews use the latest nonempty user or assistant message, including the same
successful delivery report shown in History. Migration `011_last_message_summaries.sql`
invalidates the derived user-only previews for the existing background backfill.
Session `running` reads the active turn checkpoint, independently of settled status.

Unread is a shared server-side boolean for each session, initially false. Recording
or syncing new messages sets it; successfully opening the chat clears it once per
visit. **Mark unread** keeps the chat open and stays set through refreshes until the
next visit. A dot identifies unread sessions, and the standalone, case-insensitive
`is:unread` search filter finds them, including settled sessions. `UpdateSession`
updates this flag alongside the existing name and pin metadata. The Cmd+Shift+P
command palette offers **Mark read** or **Mark unread**, according to the current chat's state.

Session discovery starts from explicitly recorded conversations, excluding private Cron locators and
recorded MCP X bindings; it does not discover orphaned entry rows or backfill
records. `CreateSession` records a fresh opaque ID with the selected loaded agent.
`ListSessions` streams one conversation per response message in list order. The
Go HTTP adapter forwards these progressively through a finite SSE response, preserving
full previews and gRPC's default 4 MiB per-message receive limit. Each envelope
carries the authenticated owner and summary completeness; the final empty envelope
marks upstream success and aggregate summary completeness, including for an empty
list. An SSE `complete` event is sent only after successful gRPC EOF. HTTP iterator exhaustion is separate from that application terminal: a
failure or wire cut can leave a visible prefix, which is not a completed list.
Cancellation cancels the gRPC call, including pending reads, and releases database
rows. `Identity` returns the Go-mapped username separately from `Protocol` negotiation.
`ListAgents` loads the current runtime definitions. Session `allowed_agents` comes
from those definitions for Web conversations and the existing Slack channel policy
for Slack Y. `$agent name` persists selection and updates the live bridge without
resetting history; private producer X cannot be selected. Removed choices are not
re-added merely because they remain the current selection.

`SettleSession` persists the sidebar grouping flag on the existing managed
conversation. Migration `008_managed_conversation_settled.sql` adds a non-null
boolean defaulting to false. Listings retain this flag after reopening; settling
does not delete entries/goals, change the agent, or cancel active execution.
`ListConfig` exposes only the retained `ConfigView` fields: workspace, overlays,
model aliases, channel agent choices, MCP server names, logging level,
auto-approver model, and enabled flags. It never serializes credentials, database
URLs, environment values, MCP commands/headers, or browser/user mappings.
`ListSkills` uses the existing rooted runtime-definition loader and returns loaded
skill text and metadata; origin is the loader's relative definition location.
With `ListSkills.agent`, it returns only skills allowed for that loaded agent,
excluding denied and review-only permissions; an unavailable agent returns no
skills. Omitting the filter retains the full skills-page catalog. The composer
supplies its selected agent and lists commands before those skills. Choosing a
skill inserts `$name `, or `$skill name ` for a built-in name collision, without
sending. Outer `$enqueue` queues its inner text unchanged, including skill
arguments, while `$stop` and `$agent` keep their built-in meanings. Normal Send
during a turn queues; Cmd/Ctrl+Enter steers. Skills are loaded when consumed.

`ListCronJobs` uses the same started Cron manager as scheduled/Slack execution.
It exposes parsed definitions and each schedule's next trigger within 24 hours,
using persisted due times for recurring schedules rather than restarting their
intervals on a read. `RunCronJob` loads the selected stem through that manager,
runs its fresh private X, and returns the actual destination Y only after the
existing runner's RunTurn and Sync finish successfully.

Historical Cron rows use existing `sync_source_entry_id` links to surviving source
entries. Backend entry reads expose generic source conversation provenance; RPC
decodes the Cron prefix and the timestamp/random suffix from the right. The stored
path label is used as-is, including previously stored underscores, without matching
current definitions. Runs are deduplicated by source X within each recorded human Y
and link only to Y. Rows sort newest-first by the source run timestamp, with Y and X
as tie-breakers. A synced row proves a copy, not successful model execution or delivery.
Recorded private Cron conversations with session entries also supply run timestamps.
When no human destination has a copy, Web shows “No delivered chat” with an
**Open chat** action. `CreateSession.source_conversation_id` accepts a recorded Cron
producer and creates or reuses `web:<source ID>`, then calls `SyncConversation`.
Creation and sync are idempotent, including concurrent opens and retries after a
failed sync. This creates a writable Web conversation, without Slack creation or
Cron execution. The Web chat ID retains its source cron ID to resolve the current definition's channel for
agent choices in the picker, sidebar, and server-side agent changes. Choices use
the current configured channel agent order intersected with loaded agents; if no
list can be computed, all loaded agents are allowed. Creation selects the first
allowed loaded agent; reopening does not overwrite a conversation's selection.
Existing delivered chat links are preserved. Authenticated users can read
Cron producer history through `History`, including runs without a delivered chat;
this does not grant prompt, queue, deletion, or other mutation access to producers.
A delivered copy takes precedence over the undelivered row for the same source.
Missing source entries cannot supply history; no migration, backfill, or history
rewrite is performed. Web groups runs beneath their definition, retaining groups
whose definition is gone. `History.source_conversation_id` optionally restricts
the authorized destination's history to one source run before replay decoding;
it does not change access to other private source conversations. The grid uses this
filter for quick previews, with a link to the destination chat.

## Attachments

`UploadAttachment(stream Attachment) -> Attachment` accepts one file per call.
The first frame supplies `conversation_id`, `name`, and optional `data`; subsequent
frames supply `data`. Each chunk is at most 256 KiB, below gRPC's default 4 MiB
receive limit. Close the sending side to finish. The response supplies the
immutable ID, basename, detected MIME type, and original byte count. Pass IDs in
`Prompt.attachment_ids` for either steer or queue. Images retain the existing
model-side normalization; all uploads are also materialized through
`os.OpenRoot(cfg.Workspace)` at `artifacts/uploads/<id>/<basename>`. Other files
are available to model tools at those paths. The original text, principal,
attachment references, and image input travel through existing durable queue
content. Prompt text includes `attachment:<id>` and the safe workspace path so
replay retains a file reference without embedding its bytes in transcript RPCs.

`DownloadAttachment(Attachment) -> stream Attachment` requires the **visible
conversation ID** in `conversation_id` and the attachment ID in `id`. It authorizes
against that conversation's current history or queued references on every call,
then returns exact original bytes in 256 KiB chunks. Merely knowing an ID is not
authorization. Private Cron producers and recorded private external MCP sessions
are denied, as are unmapped browser IPs. Deleting history revokes its references;
deleting a source also revokes its dangling synced copies. Queued references
remain valid until removed or consumed.

`History`, `Join`, and `ListQueue` carry repeated `attachments` metadata. The
protobuf JSON shape (using protojson) is:

```json
{"id":"opaque-id","name":"image.png","mimeType":"image/png","size":"1520338","originalUnverified":false,"conversationId":"producer-id"}
```

`data` is absent from transcript/queue metadata. `conversationId` there identifies
the stored producer, which can differ from the visible conversation for a synced
entry. **The HTTP proxy must use the currently viewed conversation for download
authorization, never substitute this producer field.** Standard Go `encoding/json`
uses the generated snake_case tags and a numeric `size`; the HTTP adapter uses
protojson instead. Upload raw bytes to `POST /api/UploadAttachment?conversationId=…&name=…`.
Download through `GET /api/DownloadAttachment?conversationId=…&id=…`; `download=1`
forces a download. Both paths stream chunks and cancel gRPC on disconnect.

New tool outputs persist original bytes in the configured filesystem directory or
S3 bucket before returning immutable IDs in successful tool replay. PostgreSQL
stores ownership, file metadata, and byte counts only; migration
`014_attachments.sql` creates that metadata table. The default directory is
`<workspace>/.rocketclaw/attachments` (or `.femtoclaw/attachments` for the legacy
configuration). See the root README for the `attachments` configuration.
Historical successful tool calls may recover still-available workspace files
once; recovered metadata says `original_unverified=true`. Later overwrites or
removal never replace recovered bytes. Missing or root-escaping historical files
are omitted. This is a snapshot of available bytes, not a claim to recover an old
file version. Originals are never resized in storage. Unreferenced blobs remain
stored but cannot be downloaded; garbage collection is not part of this transport.

## Start

### History summary backfill

Existing histories gain durable sidebar summaries in the background when the
RocketClaw runtime starts. Backfill does not gate readiness. It commits progress
incrementally, so a later runtime start resumes histories that still lack a
summary. It does not create conversation records or expose orphaned histories.
Normal history writes maintain the summary in the same transaction as the history.
New conversation records initialize their summary in the creation transaction,
including when they attach existing orphan history. An empty summary is complete;
a missing summary is not. Empty histories retain blank previews and display
timestamps. Sidebar enumeration reads stored summaries without scanning history,
including while legacy summaries are still missing.

If backfill fails, the runtime logs `backfill session summaries` with the error
and stops that backfill attempt; it does not retry automatically within the same
run. Investigate the logged error before restarting to resume. Shutdown cancels
and joins the backfill before releasing the runtime lock or closing the store.

### Run the Web transport

Sidebar Slack labels use stored channel facts. A background lane refreshes known
channels every three minutes; rate limits can extend staleness, and failed
lookups retain the last known facts. Sidebar requests do not wait for Slack.
Stored facts do not grant permission: action authorization still uses live checks.
Configure rename subscriptions using
[Slack Channel Rename Subscriptions](../../../../cmd/rocketclaw/CHEATSHEET.md#slack-channel-rename-subscriptions).

Build the browser assets before compiling Go. Frontend source lives in `internal/rocketclaw/web/`;
the build writes compiled assets to `internal/rocketclaw/internal/web/dist/`,
which Go embeds in the binary. `make build`, `make lint`, and `make test` build
these assets as a prerequisite. Bun 1.4.0 or newer is needed for builds and tests,
but the deployed binary needs no Bun or Next.js process.

From the repository root:

```sh
export TMPDIR="$PWD/.tmp"
(cd internal/rocketclaw/web && bun install --frozen-lockfile && bun run build)
go run ./cmd/rocketclaw
```

Web always starts. Its HTTP listener defaults to `0.0.0.0:3000`; set
`web.listen_address` in `rocketclaw.json` or `femtoclaw.json` to change it.
The HTTP adapter connects to gRPC entirely in memory inside the same process.
No Web environment variables or filesystem sockets are needed. Browser identity
still comes from the connecting IP, using `web_users` first and Tailscale WhoIs otherwise.

The normal RocketClaw command still starts its existing Slack/Cron runtime. The
isolated transport tests below do not start Slack or touch runtime configuration.

Open `/s/<base64url-conversation-id>` in the Web UI to read the conversation.
Thinking and tool traces expand inline within each turn; replies and successful
verbatim-delivery reports remain visible when the trace is collapsed.
For example, obtain the URL component with:

```sh
bun -e 'console.log(Buffer.from(process.argv[1]).toString("base64url"))' 'slack-thread:C1:1.1'
```

The `DeleteSessionEntries` RPC removes all entries for that exact conversation ID, not its conversation
or goal record. Ordinary GC remains responsible for those records.

## Identity boundary

RocketClaw accepts browser connections directly (for example over the private network).
HTTP mutations and uploads reject cross-origin browser requests using Go's
`CrossOriginProtection`; browser-IP identity never grants cross-origin authority.
The private Unix socket trusts that OS user, including other processes running as that user.
Do not expose the socket through a general-purpose relay.

The HTTP proxy forwards only the connection's remote IP. It ignores
`X-Forwarded-For`, `X-Real-IP`, and client-supplied principal headers. Do not put
another HTTP reverse proxy in front of it: that would identify the proxy rather
than the browser. Go snapshots configured `web_users` IP-to-username mappings at
startup. An explicit mapping takes precedence; otherwise Go runs `tailscale whois
--json` for the browser IP and uses `UserProfile.LoginName` as its identity.
Successful lookups are cached per IP for five minutes and shared with the Config
page. Concurrent misses are serialized; expired entries must be looked up again,
and a failed refresh denies access rather than using the stale identity.
Failed lookups, missing login names, and tagged devices without a manual mapping
are rejected with `Unauthenticated` (HTTP 401). Mapping changes require restart.
TypeScript does not read RocketClaw configuration or perform Tailscale WhoIs.
For the Config page, Go runs `tailscale whois --json` with the authenticated
browser connection IP and exposes only `UserProfile.LoginName` as `tailscale_user`.
Lookup failures leave that display field empty; a manually mapped user can still
access the Web interface. For Tailscale identification, the CLI must be available on the
RocketClaw process's PATH and connected to its local service.
`Protocol` exposes only the schema hash to the trusted local proxy and requires
no browser identity, so startup does not require mapping localhost.

## Verify without a live Slack runtime

Use a dedicated test PostgreSQL database, never a production database. Each run
creates an isolated schema via the existing test helper.

```sh
mkdir -p "$PWD/.tmp/test-tmp"
export TMPDIR="$PWD/.tmp/test-tmp"
export ROCKETCLAW_TEST_DATABASE_URL='postgres://postgres:postgres@127.0.0.1:55435/rocketclaw_test?sslmode=disable'
go test -race -count=1 -run '^(TestWebRPC|TestSessionEntries|TestPrivateSocket)$' \
  ./cmd/rocketclaw ./internal/rocketclaw/frontend/rpc
```

`TestSessionEntries` starts real Unix gRPC and the Go HTTP adapter, uses Bun as its
HTTP test client, and verifies PostgreSQL results. It checks spoofed forwarding headers,
unmapped IPv6 connections, ordering, empty results, and preservation of unrelated
entries and conversation/goal records. Bun must be installed and `web` dependencies
must already be installed. The TypeScript integration test skips when run alone;
Go provides its isolated storage fixture.

For real desktop/mobile transcript gestures, build Web first and provide an
installed Playwright module and Chromium executable:

```sh
(cd internal/rocketclaw/web && bun run build)
export ROCKETCLAW_PLAYWRIGHT_MODULE='/absolute/path/to/playwright-core/index.mjs'
export ROCKETCLAW_CHROMIUM='/absolute/path/to/chromium'
go test -count=1 -timeout=80s -v ./internal/rocketclaw/frontend/rpc
```

The browser check starts its own Next server on an ephemeral port and checks
inline thinking collapse and visible replies on desktop and at 390px width.
It stops only that test process and saves `r22-web-desktop.png` and
`r22-web-mobile.png` under the repository-local `TMPDIR`.

After changing the proto, run `go generate ./internal/rocketclaw/frontend/rpc`
with `protoc` and `protoc-gen-go` on `PATH`. This regenerates messages and the
schema hash; the network test also compares Go's hash with the TypeScript source.
