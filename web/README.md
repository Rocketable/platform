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
Saved rows show as refreshing or stale; missing summaries display `loading...`.
Failed, partial or cancelled streams do not replace the snapshot. History
deletion clears preview text without removing the row. Composer choices and
sends stay on the agent query and do not wait for sidebar refresh.
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
hydration, owner switches, and actual-App restore/merge/identity/deletion
behavior, including a connection cut after terminal metadata and protocol changes.

Slack installation requirements, including channel rename subscriptions, are in
the [RocketClaw cheatsheet](../cmd/rocketclaw/CHEATSHEET.md#slack-channel-rename-subscriptions).
