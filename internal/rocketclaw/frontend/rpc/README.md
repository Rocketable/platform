# Web conversation transport

This bounded transport implements `Protocol`, `ListSessionEntries`,
`LoadSessionEntries`, `DeleteSessionEntries`, `ListSessions`, `History`, `Prompt`, and `Join` from
`web/proto/web.proto`. Steer Prompt waits for Backend RunTurn completion, using the
authenticated username. Bare `$stop` runs cancel through RunTurn and waits for
that interrupted turn. Queue Prompt stashes waiting work through existing
Backend queue operations and returns without waiting for that turn. `ListQueue`, `SteerQueueItem`, `RemoveQueueItem`, and `ReorderQueue` use
existing Backend queue operations; promotion keeps the queued item's original
principal. Dropped items never run. Reorder writes persisted enqueue positions. Join streams only live
events for the requested conversation; it does not replay stored history or
return output through Prompt's private response field. Live events carry Backend
turn IDs so incremental answers cannot replace a preceding turn. History returns
typed user/assistant messages through its separate RPC. History and sidebar previews
share display text with one canonical Web prompt envelope removed from user
messages; stored replay, principal framing, and assistant text remain unchanged.
Session discovery starts from explicitly recorded conversations, excluding private Cron locators and
recorded MCP X bindings; it does not discover orphaned entry rows or backfill
records. `CreateSession` records a fresh opaque ID with the selected loaded agent.
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
Missing source entries cannot supply history; no migration, backfill, or history
rewrite is performed. The Runs section shows “No runs yet.” when no linked runs exist.

## Start

From the repository root, create a private socket directory and supply the same
address to RocketClaw and the Web process:

```sh
mkdir -m 700 -p "$PWD/.tmp/web-rpc"
export ROCKETCLAW_WEB_GRPC="unix:$PWD/.tmp/web-rpc/web.sock"
go run ./cmd/rocketclaw
# In another terminal, with the same ROCKETCLAW_WEB_GRPC:
cd web
bun run build
bun run start
```

Without `ROCKETCLAW_WEB_GRPC`, cmd does not start Web RPC. The Web process requires
this variable. The Go listener refuses relative paths and directories accessible
to other users. It will not overwrite an existing socket. Normal shutdown removes
the socket; after a crash, the operator must confirm the old process has stopped
before removing its stale socket.

The normal RocketClaw command still starts its existing Slack/Cron runtime. The
isolated transport tests below do not start Slack or touch runtime configuration.

Open `/s/<base64url-conversation-id>` in the retained Web UI, expand **Session
entries**, then use **List entries**, **Load entries**, or **Delete entries**.
For example, obtain the URL component with:

```sh
bun -e 'console.log(Buffer.from(process.argv[1]).toString("base64url"))' 'slack-thread:C1:1.1'
```

Delete removes all entries for that exact conversation ID, not its conversation
or goal record. Ordinary GC remains responsible for those records.

## Identity boundary

The Node Web process must run as the same OS user as Go and accept browser
connections directly (for example over the private network). The private Unix
socket trusts that OS user, including other processes running as that user.
Do not expose the socket through a general-purpose relay.

The HTTP proxy forwards only the connection's remote IP. It ignores
`X-Forwarded-For`, `X-Real-IP`, and client-supplied principal headers. Do not put
another HTTP reverse proxy in front of it: that would identify the proxy rather
than the browser. Go snapshots configured `web_users` IP-to-username mappings at
startup and rejects unknown IPs with `Unauthenticated` (HTTP 401). Configure the
actual browser IP in Go before startup; configuration changes require restart.
TypeScript does not read RocketClaw configuration or perform Tailscale WhoIs.
`Protocol` exposes only the schema hash to the trusted local proxy and requires
no mapped browser address, so startup does not require mapping localhost.

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

`TestSessionEntries` starts real Unix gRPC, invokes the actual HTTP proxy through
Bun, and verifies PostgreSQL results. It checks spoofed forwarding headers,
unmapped IPv6 connections, ordering, empty results, and preservation of unrelated
entries and conversation/goal records. Bun must be installed and `web` dependencies
must already be installed. The TypeScript integration test skips when run alone;
Go provides its isolated storage fixture.

For real desktop/mobile entry-panel gestures, build Web first and provide an
installed Playwright module and Chromium executable:

```sh
(cd web && bun run build)
export ROCKETCLAW_PLAYWRIGHT_MODULE='/absolute/path/to/playwright-core/index.mjs'
export ROCKETCLAW_CHROMIUM='/absolute/path/to/chromium'
go test -count=1 -timeout=80s -v ./internal/rocketclaw/frontend/rpc
```

The browser check starts its own Next server on an ephemeral port, opens the
retained entry panel, lists/loads entries on desktop, and confirms deletion at
390px width. It stops only that test process and saves `r22-web-desktop.png` and
`r22-web-mobile.png` under the repository-local `TMPDIR`.

After changing the proto, run `go generate ./internal/rocketclaw/frontend/rpc`
with `protoc` and `protoc-gen-go` on `PATH`. This regenerates messages and the
schema hash; the network test also compares Go's hash with the TypeScript source.
