# RocketClaw Web

The Web process serves the conversation UI and proxies browser requests to the
RocketClaw backend over a private Unix gRPC socket. Backend and Web builds must
use the same protocol schema.

See [Web conversation transport](../internal/rocketclaw/frontend/rpc/README.md)
for startup commands, socket permissions, browser IP mappings and the transport
contract. The Web process takes identity from the browser connection; forwarded
headers cannot select a user. Serve it directly to browsers rather than through
another HTTP reverse proxy.

## Local checks

From this directory, run:

```sh
bun test
bun run build
```

The build includes TypeScript checks. The entry and live HTTP integration tests
need the Go test harness and skip when Bun runs them alone. Use the isolated
PostgreSQL and browser-test instructions in the transport README to exercise
those paths. Keep temporary test artifacts under the repository's `.tmp/`.

The sidebar restores the last complete list only after live identity confirms the
Go-mapped username, keys native IndexedDB by owner and protocol, and keeps that
snapshot until a refresh is successfully exhausted with complete summaries.
Routine background refreshes are silent; stale rows and missing summaries still
show their status (`Stale` or `loading...`).
Failed, partial or cancelled streams do not replace the snapshot. History
deletion clears preview text without removing the row. Composer choices and
sends stay on the agent query and do not wait for sidebar refresh.
New Web threads select `main` by default; existing threads retain their agent.
The desktop sidebar can be hidden and reopened with the header toggle, retaining
its search and filters during navigation.
Cron runs are grouped beneath their definition, with links to their chat.
The grid shows the past and next 12 hours: click a recorded run marker or the
bar (for the latest run) to preview that run, then choose **Open chat**.
Previews include only history from the selected run. Removed definitions keep
their recorded runs visible. **Run** shows a spinner and **Running…** until
execution finishes, then opens the resulting chat.
Each turn has an inline **Thinking** disclosure, expanded by default, containing
reasoning summaries and tool traces. Replies and successful verbatim-delivery
reports appear as normal messages outside that disclosure. The chat has no
database-entry inspection panel.
Each tool call has one labeled, initially expanded disclosure containing its
arguments and matching result. Loaded skill instructions fold with their skill
call. Long results have a collapse control at the bottom as well as the header.
The transcript has a visible scrollbar and a side rail with one jump marker per
turn. Each marker is labeled with its prompt; selecting it pauses automatic
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

Slack installation requirements, including channel rename subscriptions, are in
the [RocketClaw cheatsheet](../cmd/rocketclaw/CHEATSHEET.md#slack-channel-rename-subscriptions).
