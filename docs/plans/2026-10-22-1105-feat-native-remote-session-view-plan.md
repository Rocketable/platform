---
title: Native Remote Session View - Plan
type: feat
date: 2026-10-22
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Native Remote Session View - Plan

## Goal Capsule

- **Objective:** A RocketClaw Web user opens a remote OpenCode session and works in it with nearly all of OpenCode's web features, natively inside RocketClaw's page, without per-server relay ports, frames, or OpenCode logins, while the copied UI stays mergeable with each upstream v2 release.
- **Means:** Copy OpenCode v2.1.3's Solid web UI into an isolated package, `internal/rocketclaw/oc/`, mount its single-session view in RocketClaw's page, and reach each server through one same-origin proxy route (KTD1, KTD4, KTD8).
- **Authority:** Product Contract Requirements win on behavior; Key Technical Decisions win on mechanism within them; units override neither. Root `AGENTS.md` rules bind every unit (no `git`, no subqueries, no nil-as-disabled dependencies, mockery v3, error naming, CLOC budgets never edited). `internal/rocketclaw/oc/AGENTS.md`, created in U1, binds every edit to copied upstream code.
- **Stop conditions:** Stop and report when the Go source budget (25000) or the Web TS budget (8000) would be exceeded; when a needed edit to copied upstream code cannot be kept to a marked, local change (KTD6); or when upstream v2.1.3 behavior contradicts the research in Sources.
- **Execution profile:** New Bun/Vite package with copied upstream TypeScript, Go HTTP and backend changes, one proto change, React Web changes; one PR.
- **Finish and ship:** `ce-work` implements and verifies. Committing, pushing, and opening the PR follow the user's chosen route. Merging stays with the user.

---

## Product Contract

### Summary

RocketClaw serves its own copy of OpenCode's web UI and shows a remote session natively in the RocketClaw tab that opens it. Every OpenCode web feature stays except the window tab bar, OpenCode's home, project and session lists, its server manager, and desktop-only extensions. One same-origin route proxies each configured server and adds its password. The per-server relay ports and the frame are removed. Upstream's tagged v2 releases are later merged into the copy by a separate sync loop.

### Problem Frame

Opening a remote OpenCode session today frames OpenCode's entire web app through a relay that listens on its own port per server (`internal/rocketclaw/frontend/rpc/relay.go`, `RemoteSession` in `internal/rocketclaw/web/src/ui.tsx`). Each server needs a dedicated port, tailnet ACL entries, and frame-ancestor headers. The framed app carries its own tab bar, home page, and server manager, which duplicate RocketClaw's tabs, sidebar, and configuration. Keyboard focus, theme, and shortcuts stop at the frame edge. The user wants a slimmer, native experience that keeps OpenCode's session features. The copy must be isolated enough that an automated loop can keep it current with upstream releases, which ship several times a week.

### Requirements

**Session view**

- R1. Opening a remote session in RocketClaw shows OpenCode's session view natively in that RocketClaw tab, with no frame.
- R2. The view keeps every OpenCode web feature except those in R3: timeline and message rendering, composer (attachments, mentions, slash commands, shell mode, queue and steer), agent and model pickers, permission and question prompts, review and diffs, file tree and viewer (including Microsoft Office previews), terminal, settings, provider connection, revert, fork and export, and the context, details, debug, and btw extensions.
- R3. The view omits OpenCode's window tab bar and its tab commands, its home page, project list and session lists, its server manager, and the desktop-only extensions (browser pane, SSH, WSL, updater, pairing).
- R4. The view renders with OpenCode's look; its light/dark mode follows RocketClaw's, OpenCode's own light/dark switch is hidden, and its color-theme picker stays.
- R5. The view opens any session on a configured server, including one not yet in the sidebar; OpenCode reports unknown sessions itself.

**Navigation inside the view**

- R6. Forking a session, starting a new session, or opening a session referenced in the conversation opens that session in its own RocketClaw tab; a new session is created in the current folder through RocketClaw's existing "New on <server>" path, so it appears in the sidebar.
- R7. Child (subagent) sessions open inside the same view, and going back returns to the parent.
- R8. Deleting the session from inside the view closes its RocketClaw tab; clicking the project name does nothing.

**Keyboard**

- R9. ⌘P and ⌘⇧P open RocketClaw's palettes; OpenCode's palette opens with ⌘K only.
- R10. RocketClaw's shortcuts win every clash with OpenCode's; the OpenCode commands that lose their shortcut stay reachable from OpenCode's ⌘K palette.
- R11. While OpenCode's terminal has focus, RocketClaw's shortcuts go to the terminal instead of RocketClaw; OpenCode's own terminal key handling, including its toggle shortcut, is unchanged.
- R12. Escape pressed inside the view never closes the RocketClaw tab.

**Server access**

- R13. The browser reaches each configured OpenCode server only through one same-origin route on the Web listener, which adds the server's password; the browser never holds it.
- R14. The route admits exactly the callers the Web admits (`web_users` or a Tailscale user), refuses cross-origin requests including WebSocket upgrades, and serves OpenCode's HTTP API, event stream, and terminal WebSocket.
- R15. Per-server relay ports, the frame, and the `port` field of each `opencode` config entry are removed; existing config files that still set `port` keep loading.

**Server state shown in the view**

- R16. When a server's OpenCode major.minor version differs from the copy's, a small pill at the top of that session's view names both versions, and the session still opens.
- R17. While the view is open, a server that rejects RocketClaw's password, or cannot be reached, shows that state inside the view without a browser sign-in prompt; a reachable-again server resumes without a reload.

**RocketClaw actions**

- R18. A RocketClaw palette command copies the displayed session's address on its own OpenCode server (the server's origin, deep-linked to the session, without the password).
- R19. Session names sync both ways: naming a remote session in RocketClaw changes its title on the server, and a title changed in OpenCode shows in RocketClaw after the next sync; the server's title is the only stored name.
- R20. RocketClaw-only names already stored for remote sessions are pushed to their servers as titles once, then cleared locally.
- R21. A RocketClaw palette command opens OpenCode's palette, so touch users reach OpenCode's commands without ⌘K.

**Copy and isolation**

- R22. The copied UI lives in `internal/rocketclaw/oc/` with its own build, its own line count reported separately from the Web's and Go's, and a record of the upstream release it came from.
- R23. Every OpenCode package the view needs is copied into the repo; no `@opencode/*` package comes from npm.
- R24. The built view is committed like the Web bundle, so a plain `go build` needs no Node or Bun.
- R25. No file or directory RocketClaw adds carries "opencode" in its name, except upstream files whose names the code looks up at runtime.

### Key Decisions

- **Native view, no frame.** (session-settled: user-directed — chosen over a same-origin frame: frames make the integration unreliable, and the cost of fencing upstream's page-wide behavior is accepted.) Governs R1, R12.
- **Everything except the tab bar, OpenCode's lists, the server manager, and desktop-only extensions.** (session-settled: user-directed — chosen over a narrower feature set: the user wants OpenCode's session features intact.) Governs R2, R3.
- **Microsoft Office previews stay.** (session-settled: user-directed — chosen over dropping the extension to save its size and dependencies.) Governs R2.
- **RocketClaw wins shortcut clashes; OpenCode's palette moves to ⌘K.** (session-settled: user-directed — chosen over sharing ⌘P or ⌘⇧P with OpenCode.) Governs R9, R10.
- **The terminal is exempt while focused.** (session-settled: user-approved — chosen over RocketClaw always winning: Ctrl+P, Ctrl+B and Ctrl+[ are everyday terminal keys on Linux.) Governs R11.
- **OpenCode's look, following RocketClaw's light/dark mode.** (session-settled: user-approved — chosen over OpenCode's own theme settings and over restyling to RocketClaw's look, which would conflict on every merge.) Governs R4.
- **The tunnel is dropped for a same-origin route.** (session-settled: user-directed — chosen over keeping per-server relay ports.) Governs R13, R15.
- **Version mismatch warns discreetly and still opens.** (session-settled: user-directed — chosen over refusing to open and over silence; the warning is a pill on that session's view, not an app-wide banner.) Governs R16.
- **Navigation landings.** (session-settled: user-approved — fork, new, and referenced sessions get RocketClaw tabs; children stay inside; delete closes the tab.) Governs R5, R6, R7, R8.
- **Bi-directional name sync with the server as the source.** (session-settled: user-directed — chosen over RocketClaw-only names and over a one-way server rename; this reverses R7 of `docs/plans/2026-10-17-1906-feat-remote-opencode-sessions-plan.md`, where a rename changed only RocketClaw's name.) Governs R19, R20.
- **Copy-address command.** (session-settled: user-directed — added by the user so a session can be opened elsewhere.) Governs R18.
- **Committed build.** (session-settled: user-approved — chosen over building during `make` only: a plain `go build` keeps working without Bun.) Governs R24.

### Success Criteria

- A person can open, prompt, steer, approve a permission, open a file, review a diff, and use the terminal in a remote session without leaving RocketClaw's tab, and nothing outside that tab changes appearance.
- Leaving and reopening remote sessions repeatedly leaves no OpenCode styles, listeners, streams, or sockets behind.
- A later sync-loop run can three-way merge a new upstream v2 release into `internal/rocketclaw/oc/` file by file, using only the recorded tag and the upstream checkout.

### Scope Boundaries

- Not built: OpenCode's tab bar, home, project and session lists, server manager, and desktop-only extensions (R3).
- Not built: a per-user OpenCode login; anyone the Web admits still controls every configured server, including its shell.
- Not built: OpenCode system notifications; RocketClaw's existing notifications cover remote sessions and OpenCode keeps its sounds. Evidence that people miss OpenCode-specific alerts would change this.
- Not built: gating RocketClaw's hidden transcript stream or deferring the view's mount during palette preview. The deployment is assumed to run HTTP/2 or better (KTD13); evidence of HTTP/1.1 use would change this.
- Not built: rejecting `opencode` server names `.` and `..`, which pass name validation but cannot be reached in a URL path. An operator sees such a server fail at once.
- Not built: upstream's own tests that need its Storybook package or a live OpenCode server; copied unit tests that run in isolation are kept (U1).
- Not built: hiding RocketClaw's dollar commands for remote sessions. `$undo` and `$redo` already stay hidden there, and `$stop` behaves as it does today.
- Not built: a custom notice for a failed load of the view's bundle. The entry name is stable, and RocketClaw's existing protocol guard reloads stale pages.

#### Deferred to Follow-Up Work

- The sync loop that merges each new upstream v2 release into the copy, using the manifest from U1.
- A line budget for `internal/rocketclaw/oc/`, set by the user after the first port's size is known (R22).

### Sources

- Research dossiers for this plan (local, not committed): repository patterns, learnings, upstream shell, upstream dependencies, and user-flow analysis.
- `docs/plans/2026-10-17-1906-feat-remote-opencode-sessions-plan.md` — the relay and sync this plan replaces in part.
- `docs/solutions/best-practices/react-doctor-complexity-limit-shapes-web-components.md` — `SessionApp` and `CommandPalette` sit at react-doctor's complexity limit.
- `docs/solutions/best-practices/wrapcheck-cross-package-helper-error.md` — wrap cross-package errors.
- `docs/solutions/logic-errors/permission-review-reasons-read-from-transport-specific-trace.md` — test both the plain and tailnet listeners.
- Upstream OpenCode `anomalyco/opencode` tag `v2.1.3` (commit `bb25a2f4a3f3`), read from the colocated local checkout.

---

## Planning Contract

### Key Technical Decisions

- KTD1. **Keep upstream's Solid code; do not rewrite it in React.** The copy mounts into RocketClaw's React page through a framework-neutral `mount` entry (KTD8). (session-settled: user-approved — chosen over a React rewrite: translating about 108k lines of Solid by hand on every upstream release is not sustainable.)
- KTD2. **The copy lives in `internal/rocketclaw/oc/` with its own line count; a budget is set after the first port.** It has its own `go.mod` stub (like `internal/rocketclaw/web/go.mod`) so Go scans skip it, its own `.gitignore`, `package.json`, `bun.lock`, and `Makefile`. (session-settled: user-directed — chosen over a subfolder of `internal/rocketclaw/web/`, whose line count and lint would include it.) RocketClaw's own glue code inside the package lives under `internal/rocketclaw/oc/host/` and is reported separately from copied upstream code, so it is never hidden in the upstream count.
- KTD3. **Every OpenCode package the view reaches is copied; third-party libraries stay npm dependencies pinned to upstream's lockfile versions.** The runtime closure is `app`, `session-ui`, `gui-extensions`, `ui`, `client`, `schema`, and `util`. `protocol` is not reached at runtime, and `plugin-browser` only serves the dropped browser extension, so neither is copied. Only the files the browser reaches are copied from `util`, `client`, and `schema`. Upstream's dependency patches for libraries the view uses (`solid-js`, `@tanstack/virtual-core`, `@pierre/trees`, `@dnd-kit/dom`, `@corvu/drawer`, `ghostty-web`, `vite`) are copied with them. (session-settled: user-directed — chosen over installing the published `@opencode/client`, `@opencode/schema`, and `@opencode/ui` from npm.)
- KTD4. **Copied files keep upstream's paths under `internal/rocketclaw/oc/packages/<name>/`.** Each copied file then maps one-to-one to its upstream path, which is what a file-by-file three-way merge needs. Files RocketClaw deletes from the copy are listed in the manifest (KTD5), so the loop knows not to re-add them.
- KTD5. **Edits are made directly in the copied files; `internal/rocketclaw/oc/manifest.json` records the upstream repository, tag, commit, and the copied, renamed (upstream path → copy path), and deleted paths.** A later release comes in by three-way merging old tag, new tag, and the copy per file, with the old tag's tree read from the upstream checkout. (session-settled: user-approved — chosen over keeping untouched upstream copies plus stored patch files: one copy of the code, normal editing, conflicts still land exactly on RocketClaw's edits.) Tags are release side commits off upstream's `dev` branch, so the merge base is the old tag's tree, never tag ancestry.
- KTD6. **Edits to copied upstream code are minimal and marked.** Each edit carries a short `rocketclaw:` comment naming why. Where an edit can live in `host/` instead (theme bridge, key routing, mount, version pill), it does. `internal/rocketclaw/oc/AGENTS.md` states these rules for future editors and the sync loop. (session-settled: user-directed — the user asked for guardrails in an `AGENTS.md` inside the package.)
- KTD7. **The package builds with Vite, upstream's own build tool, run by Bun.** Bun 1.4.4 cannot compile Solid JSX, and upstream relies on Vite-only features (`import.meta.glob`, `?raw`, `?inline`, `?worker&url`, `new URL` workers, `import.meta.env`, Tailwind 4 through `@tailwindcss/vite`). Matching upstream's build keeps those files unedited and mergeable. The config starts from `packages/app/vite.config.ts` and `vite.js`, minus the PWA, Sentry, desktop-icon, and dropped-extension plugins.
- KTD8. **The view mounts through one framework-neutral function exported by `host/`: `mount(element, options) → { dispose, openPalette }`.** Options carry the server name, the proxy base URL, the session ID, and host callbacks: open a session in a RocketClaw tab, start a new session in a folder, close this tab, and report the displayed session. `openPalette` serves R21. React loads the built entry with a runtime dynamic import and never imports Solid source, so the Web's `tsc`, oxlint, and react-doctor never see Solid JSX. Inside, `mount` renders `PlatformProvider` → `AppBaseProviders` → `AppInterface` with one server and a `MemoryRouter` seeded with the session route, following upstream's e2e fixture and desktop router.
- KTD9. **Upstream's package specifiers (`@opencode/ui`, `@opencode/client`, and so on) are kept and resolved to the copied directories by tsconfig `paths` and Vite aliases.** Renaming would touch 1,507 imports in 555 files and conflict on every merge. No `node_modules/@opencode` directory exists, because the specifiers resolve through aliases. Runtime contract strings (storage keys, the `x-opencode-ticket` and `x-opencode-directory` headers, DOM event names) stay as upstream has them. The only upstream files whose names contain "opencode" and stay are the provider icons `opencode.svg` and `opencode-go.svg`, which the icon sprite looks up by provider ID. `opencode-logo.tsx` is renamed in U1 and the manifest records the mapping (R25).
- KTD10. **One proxy route, `/oc/{server}/api/{path...}`, registered inside `rpc.NewHTTPHandler` and only under `/api/`.** It replaces `NewOpenCodeRelay`. Registering inside the handler means the route inherits the principal metadata wrapper and `NewTailnetHandler`. Admission calls the existing `Identity` RPC with the request context, so one check covers `web_users` and Tailscale on both listeners. Routes are registered per HTTP method so they do not collide with `GET /`. The proxy behaves as follows:
  1. Refuse any request whose `Origin` is present and is not the page's own origin, including `null` and WebSocket upgrades.
  2. Rewrite `Origin` to the upstream origin, so OpenCode's terminal origin check passes.
  3. Strip the `/oc/{server}` prefix.
  4. Set Basic auth `opencode:<password>`.
  5. Drop `Cookie` going upstream, and drop `Set-Cookie` and `WWW-Authenticate` coming back.
  6. Flush immediately, so the event stream streams.
  7. Mark responses `Content-Security-Policy: sandbox` and `X-Content-Type-Options: nosniff`, as `/pages-raw/` does. A proxied file opened directly then cannot run as RocketClaw.
- KTD11. **The built view is embedded from a separate directory, `internal/rocketclaw/internal/web/oc-dist/`, and served at `/oc-ui/` by the existing asset handler in `internal/rocketclaw/internal/web/assets.go`.** The Web's `build.ts` wipes `dist/`, and Sentry and asset tests glob `dist/assets/`, so the oc build must not share it. The directory name contains `dist`, so line-count scripts skip build output. The embed uses the `all:` prefix, because Vite can emit chunks whose names start with `_`, which a plain directory embed skips. Assets are served with the same `no-cache` rule as the Web bundle. (Committing it is session-settled under R24.)
- KTD12. **`ListSessionsResponse.opencode_servers` changes from server name → relay port to server name → server origin (scheme, host, and port only).** The browser needs the origin for the copy-address command (R18); the port no longer exists. The password, and any userinfo, path, or query in the configured URL, never leave the backend. Changing the proto changes the protocol hash, so open pages get the existing reload prompt.
- KTD13. **The deployment is assumed to serve RocketClaw over HTTP/2 or better.** Same-origin streams then share one multiplexed connection, and the HTTP/1.1 six-connection limit does not apply. (session-settled: user-directed — chosen over designing for HTTP/1.1's six-connection limit.)
- KTD14. **Tagged v2 releases only, starting at v2.1.3.** (session-settled: user-approved — chosen over tracking `dev`: releases match what servers run.) The version pill (R16) compares the copy's major.minor, which `host/` reads from the manifest at build time, with the server version that upstream's health state already polls from `/api/info` (`packages/app/src/runtime/server/health.ts`).
- KTD15. **Page-wide behavior is fenced, preferably at build time or in `host/`, and fully undone on dispose.** "The view" below means the mount container plus a portal root `host/` creates beside it with the same scope selector and theme attributes. The fences:
  - CSS scoping happens at build time: a RocketClaw-owned PostCSS step in `internal/rocketclaw/oc/vite.config.ts`, run after Tailwind, rewrites `:root`, `:host`, `html`, `body`, and top-level `*` selectors in all bundled CSS to the view's scope selector. Upstream CSS files stay unedited, and the step also covers files added by later releases.
  - Layout breakpoints follow the container: the container is a named size container, a host-owned CSS file redefines Tailwind's `sm`/`md`/`lg`/`xl` variants as container queries against it, the same step converts plain `@media` width rules, and every `createMediaQuery` width check in the copied packages reads one host container-width signal.
  - Overlays stay in scope: the `packages/ui` overlay components and direct `Portal` users default their mount to the portal root.
  - Everything that reads or writes `html` uses the container instead: the theme context and theme loader, the diff and file runtime, Mermaid, app icons, extension embeds, the language provider's `lang` and `dir`, and the font variables set by settings. The only edit to upstream theme code is its runtime `:root {` style string.
  - The `oc_locale` cookie is replaced by OpenCode's existing localStorage language setting.
  - The document-level keydown and file-drop listeners act only on events inside the view, or targeting `document.body` while the view is mounted, which is where focus lands after the composer blurs.
  - `dispose` removes injected styles, the portal root, listeners, workers, streams, and sockets, and flushes persisted drafts.
- KTD16. **Bi-directional names use the server title as the only stored name.** For a remote session, `UpdateSession` with a name sends `PATCH /api/session/{id}` with `title` to its server, then updates the stored title; it never writes the RocketClaw-only name. The syncer already stores each server's title. On each sync, a remote row that still has a RocketClaw-only name is pushed once as the server title and cleared after the server accepts it (R20). A server that is offline keeps the name until it accepts it.
- KTD17. **The host decides navigation through the mount callbacks (KTD8).** Upstream calls that used window tabs or the home route map as follows:
  - Fork and `tabs.addSessionTab` call open-session (R6).
  - `tab.new` and the `/new-session` route call new-session with the current folder (R6).
  - `navigateAfterRemoval` and `tabs.newDraft` call close-tab (R8).
  - The project-name click in `session-identity-header.tsx` no longer navigates, and the `/` route stub calls no callback (R8).
  - `common.goBack` and `common.goForward`, which settings uses to leave `/settings`, are registered by `host/` against the `MemoryRouter` history with no default keybind.

  `TabsProvider` stays, and a small `host/` component creates the session's tab entry on mount. That entry stores the review, file, and terminal panel state the session view needs.

### High-Level Technical Design

Components and trust boundaries:

```mermaid
flowchart TB
  subgraph Browser["Browser: RocketClaw Web page (one origin)"]
    R[React Web app: sidebar, tabs, palettes]
    O["oc view (Solid), mounted in a tab's container"]
    R -->|"runtime import /oc-ui/ entry, mount(el, options)"| O
    O -->|"host callbacks: open tab, new session, close tab"| R
  end
  R -->|"RPC: ListSessions (server origins), UpdateSession (name), CreateSession"| W[Web HTTP handler]
  O -->|"/oc/server/api/... HTTP, event stream, terminal WebSocket"| P[Same-origin proxy route]
  W --> A[Admission: Identity RPC, web_users or Tailscale]
  P --> A
  P -->|"Basic auth, Origin rewrite, prefix stripped"| S[Remote OpenCode server]
  B[Backend syncer] -->|"poll sessions; PATCH titles"| S
  B --> DB[(opencode_sessions)]
  W -->|"embedded oc-dist at /oc-ui/"| R
```

Mount lifecycle for one remote session tab:

```mermaid
sequenceDiagram
  participant T as RocketClaw tab (RemoteSession)
  participant H as oc host mount
  participant U as Upstream session view
  participant P as /oc/server/api
  T->>H: import entry, mount(container, server, base URL, session, callbacks)
  H->>H: portal root, theme from RocketClaw mode, key and drop fences
  H->>U: render providers, MemoryRouter at session route, create tab entry
  U->>P: health (/api/info), session, messages, event stream, terminal WebSocket
  H-->>H: pill if the health state's major.minor differs (still continue)
  Note over U,P: 401 replaces the view with a notice; unreachable shows a banner over the still-mounted session
  U->>H: fork, new, referenced session, delete
  H->>T: host callback opens a RocketClaw tab or closes this one
  T->>H: dispose() on leaving the tab
  H->>H: flush drafts, close streams and sockets, remove styles, listeners, portal root
```

Key routing for a keydown while the view is shown:

```mermaid
flowchart TB
  K[keydown] --> TF{Target inside oc terminal?}
  TF -->|yes| TERM[RocketClaw skips; the terminal and OpenCode's own terminal handling get the key]
  TF -->|no| HK{RocketClaw shortcut? P, Shift+P, B, Alt+N, brackets}
  HK -->|yes| RC[RocketClaw handles it at window capture; oc ignores prevented events]
  HK -->|no| ESC{Escape?}
  ESC -->|"yes, on a remote session route"| OCE[oc handles it; RocketClaw never closes the tab]
  ESC -->|no| IN{Target inside the view, or body while mounted?}
  IN -->|yes| OCK[oc keybinds, palette on mod+K]
  IN -->|no| RCO[RocketClaw only]
```

Name sync:

```mermaid
sequenceDiagram
  participant Web as RocketClaw Web
  participant BE as Backend
  participant S as OpenCode server
  Web->>BE: UpdateSession(name) on a remote session
  BE->>S: PATCH /api/session/id title
  BE->>BE: store title
  Note over BE,S: every sync: rows with a leftover RocketClaw-only name
  BE->>S: PATCH title (once)
  BE->>BE: clear local name after success
  S-->>BE: sync reads titles changed inside OpenCode
```

### Output Structure

```text
internal/rocketclaw/oc/
  AGENTS.md            guardrails for edits and merges (KTD6)
  manifest.json        upstream repo, tag, commit, copied, renamed, and deleted paths (KTD5)
  Makefile             build, lint, test, cloc
  go.mod               keeps Go scans out (KTD2)
  .gitignore
  package.json, bun.lock, tsconfig.json, vite.config.ts
  patches/             upstream's npm dependency patches (KTD3)
  host/                RocketClaw glue: mount, theme bridge, fences, scope CSS, pill, tab entry, navigation (KTD8, KTD15, KTD17)
  packages/
    app/ session-ui/ gui-extensions/ ui/ client/ schema/ util/   copied upstream paths (KTD4)
internal/rocketclaw/internal/web/oc-dist/   committed build output (KTD11)
```

### Assumptions

- HTTP/2 or better serves the Web in every deployment (KTD13).
- Servers run OpenCode v2; a major-version change is out of scope for the pill and the merge loop.
- `GET /api/info` reports the server version in every v2 release (`packages/protocol/openapi.json` at v2.1.3).

---

## Implementation Units

### U1. Copy upstream v2.1.3 into `internal/rocketclaw/oc/` and build it

- **Goal:** The unmodified upstream app builds from the copy and its isolated unit tests pass, before any RocketClaw edits.
- **Requirements:** R22, R23, R24, R25; KTD2, KTD3, KTD4, KTD5, KTD6, KTD7, KTD9, KTD11.
- **Dependencies:** none.
- **Files:**
  - Create: `internal/rocketclaw/oc/{AGENTS.md,manifest.json,Makefile,go.mod,.gitignore,package.json,bun.lock,tsconfig.json,vite.config.ts}`, `internal/rocketclaw/oc/patches/`, and `internal/rocketclaw/oc/packages/{app,session-ui,gui-extensions,ui,client,schema,util}/` (copied subsets).
  - Modify: `internal/rocketclaw/internal/web/assets.go` (second `all:` embed and `/oc-ui/` serving, KTD11), `internal/rocketclaw/Makefile`, root `Makefile` (`cloc`), `.github/workflows/test.yml` (install, cache keyed on `internal/rocketclaw/oc/bun.lock`, build, committed-output check).
  - Test: `internal/rocketclaw/internal/web/assets_test.go`.
- **Approach:**
  1. Copy the runtime closure listed in KTD3 at tag `v2.1.3`, at upstream paths, using `jj file show`/`jj file list` against the upstream checkout. Include each copied module's unit tests and the test preloads (`packages/ui/test/solid-jsx.ts`, happy-dom).
  2. Rename `packages/app/src/providers/opencode-logo.tsx` (R25, KTD9) and update its imports with `rocketclaw:` markers.
  3. Write `manifest.json` with repo URL, tag, commit `bb25a2f4a3f3`, the copied paths, and the rename mapping.
  4. Resolve every `catalog:` and `workspace:*` dependency to the exact version in upstream's `bun.lock`. Use a single version where upstream nests two (Vite, Tailwind, vite-plugin-solid): app's.
  5. Build with Vite (KTD7) into `internal/rocketclaw/internal/web/oc-dist/`, with stable output names for the entry so React can import it.
  6. Write `AGENTS.md`:
     - Marked minimal edits.
     - `host/` first.
     - Never rename upstream paths or package specifiers.
     - Keep the manifest current.
     - Cite the upstream path in each edit's comment.
     - The merge procedure outline.
     - The temp-directory and no-`git` rules from the root `AGENTS.md`.
  7. The `Makefile` mirrors `internal/rocketclaw/web/Makefile`: `build`, `lint` (`tsc --noEmit` with Solid JSX settings), `test` (bun unit tests), and `cloc` reporting copied and `host/` lines separately. No `check-cloc-budget` until the user sets one.
- **Execution note:** Prove build parity first: the copy, unchanged except aliases, build config, and the R25 rename, must build and pass its unit tests before U2 touches it.
- **Patterns to follow:** `internal/rocketclaw/web/Makefile`, `internal/rocketclaw/web/go.mod`, `internal/rocketclaw/internal/web/assets.go`.
- **Test scenarios:**
  - Copied upstream unit tests for `client`, `schema`, `util`, `ui`, `session-ui`, `gui-extensions`, and `app` run with `make -C internal/rocketclaw/oc test` and pass.
  - `assets_test.go`: every file in `oc-dist`, including any whose name starts with `_`, is served under `/oc-ui/` with `Cache-Control: no-cache`.
  - Building twice from a clean `node_modules` yields byte-identical `oc-dist`.
  - `rg` finds no RocketClaw-added file or directory named with "opencode" (R25), except the two provider icons.
- **Verification:** The build succeeds from a clean install, the output is reproducible, `make cloc` reports the package separately, and Go scans and Web lint ignore the package.

### U2. Trim the copy to the single-session product

- **Goal:** The copy contains only the features R2 keeps, with upstream's own behavior otherwise unchanged.
- **Requirements:** R2, R3, R9, R10; KTD5, KTD6, KTD17.
- **Dependencies:** U1.
- **Files:**
  - Delete from the copy and list as deleted in `internal/rocketclaw/oc/manifest.json`:
    - `packages/app/src/{home,new-session,servers}/` and `packages/app/src/settings/servers/`
    - the tab-bar UI under `packages/app/src/shell/titlebar/` and `shell/tabs` UI files
    - `packages/app/src/desktop.ts`
    - `packages/app/src/entry.tsx` (`host/` replaces it)
    - `packages/gui-extensions/src/{browser,ssh,wsl,updater,pairing}/`
  - Modify:
    - `packages/gui-extensions/src/renderer.ts`
    - `packages/app/src/shell/{shell.tsx,routes/routes.tsx,commands/command.tsx}`
    - `packages/app/src/session/commands/{use-session-commands.tsx,fork-dialog.tsx}`
    - `packages/app/src/session/session-identity-header.tsx`
    - `packages/app/src/session/requests/session-question-dock.tsx`
    - `packages/app/src/composer/attachments/destination.ts`
    - `packages/app/src/runtime/server/request-queue.ts`
    - `packages/app/src/settings/shell.tsx`
    - `packages/app/src/runtime/platform/web.ts`
  - Test: copied tests next to each touched module, updated or deleted with the code they cover.
- **Approach:**
  1. Remove the dropped extensions from `renderer.ts`, the single composition point, so their code and dependencies leave the bundle.
  2. Remove the titlebar from `Shell`. Keep `TabsProvider` and the toast hosts.
  3. Route upstream's tab and home navigation to the host callbacks as KTD17 maps them; the `/` stub calls nothing and `/new-session` calls new-session.
  4. Remove the server-manager entry points from settings.
  5. Set `DEFAULT_PALETTE_KEYBIND` to `mod+k`. Default `file.open` and `session.background` to no binding, and `session.question.back` to `alt+arrowleft` on every platform. Remove tab commands with the titlebar.
  6. Fix the two prefix bugs:
     - `destination.ts` builds an absolute `/api` URL; resolve it relative to the server base URL instead, and drop its own Basic auth.
     - `request-queue.ts` compares the exact `/api/event` path; match the path suffix instead.
  7. Make `platform.notify` inert. Keep sounds.
  8. Drop the service worker, PWA, and Sentry wiring.
- **Patterns to follow:** upstream's own composition in `gui-extensions/src/renderer.ts` and the `router` prop in `app.tsx`.
- **Test scenarios:**
  - Rendering the session route with the trimmed shell shows no titlebar and no tab strip; toasts still render.
  - The command registry contains no `tab.*` or `home.toggle` command, and `file.open` has no default keybind.
  - No default OpenCode keybind matches a RocketClaw shortcut: mod+P, mod+Shift+P, mod+B, Ctrl+B, mod+Alt+N, mod+[, or mod+].
  - The palette opens on mod+K and not on mod+Shift+P.
  - With a server base URL of `http://host/oc/box/`, an attachment upload posts to `http://host/oc/box/api/...`.
  - With that base URL, the request queue classifies `/oc/box/api/event` as the event stream.
  - The built bundle contains no `plugin-browser`, SSH, WSL, updater, or pairing modules, and still contains the Office preview chunks.
- **Verification:** `make -C internal/rocketclaw/oc build test lint` passes, and every edit carries a `rocketclaw:` comment.

### U3. Host mount and page-sharing fences

- **Goal:** `mount(element, options)` shows one remote session natively, fenced from the rest of the page, and `dispose` leaves nothing behind.
- **Requirements:** R1, R4, R5, R7, R10, R11, R12, R16, R17, R21; KTD8, KTD9, KTD14, KTD15, KTD17.
- **Dependencies:** U2.
- **Files:**
  - Create:
    - `internal/rocketclaw/oc/host/{mount.tsx,theme.tsx,fences.ts,version-pill.tsx,session-tab.tsx,state.tsx,navigation.tsx}`
    - `internal/rocketclaw/oc/host/scope.css` (container queries and the top-right inset)
  - Modify:
    - `internal/rocketclaw/oc/vite.config.ts` (the CSS selector-rewrite step, KTD15)
    - `packages/ui/src/theme/{context.tsx,loader.ts}` (container target)
    - the `packages/ui` overlay components (dialog, popover, select, tooltip, menu, context menu, hover card, toast) and other direct `Portal` users (portal root)
    - `packages/session-ui/src/pierre/file-runtime.ts`, `packages/session-ui/src/components/markdown-mermaid.ts`, and the app icon and extension embed modules (read the container, not `html`)
    - `packages/app/src/runtime/i18n/language.tsx` (no cookie; `lang` and `dir` on the container)
    - `packages/app/src/settings/model.tsx` (font variables on the container)
    - `packages/app/src/shell/commands/command.tsx` (ignore prevented and out-of-view events)
    - `packages/app/src/composer/attachments/attachments.ts` (drop scope)
    - `packages/app/src/session/composer/region.tsx` (autofocus scope)
    - every `createMediaQuery` width check in the copied packages (container width)
    - `packages/app/src/shell/routes/routes.tsx` (401 and unreachable states)
    - `packages/app/src/settings/general/controllers.ts` (hide the light/dark switch)
  - Test: `internal/rocketclaw/oc/host/mount.test.tsx`, `internal/rocketclaw/oc/host/fences.test.ts`.
- **Approach:**
  1. `mount` renders upstream's provider stack with one server whose URL is the page origin plus `/oc/<server>`, with no password and no `canonicalLocalServer`. Storage is then scoped per server, and `displayName` is the server name.
  2. A `MemoryRouter` is seeded with the session route (KTD8). Child sessions navigate inside it (R7), and each displayed-session change calls the host's report callback.
  3. `theme.tsx` watches RocketClaw's `dark` class on `html` and calls `setColorScheme` (R4).
  4. `fences.ts` applies and reverts KTD15 at runtime: the portal root, the container-width signal, and the event fences. Upstream's document keydown handler returns early on `defaultPrevented` and on events outside the view (KTD15); it is not otherwise changed for the terminal, so OpenCode's own terminal key handling stays as upstream ships it (R11).
  5. `version-pill.tsx` reads the server version from upstream's health state and shows both versions when major.minor differ (R16, KTD14).
  6. `state.tsx` replaces upstream's connect screen:
     - On 401 it replaces the view with "RocketClaw cannot sign in to <server>".
     - When the server is unreachable it shows a non-blocking "reconnecting" banner over the still-mounted session, keeping the timeline readable and the composer draft intact.
     - Health polling clears either state when the server answers again (R17).
  7. `session-tab.tsx` creates the tab entry, and `navigation.tsx` registers `common.goBack` and `common.goForward` with no keybind (KTD17).
  8. `scope.css` insets the session view's top-right actions by 3rem at the `lg` breakpoint, so RocketClaw's fixed theme toggle never covers OpenCode's review toggle.
  9. `mount` returns `openPalette()` with `dispose` (KTD8, R21).
  10. `dispose` flushes persisted stores before tearing down.
- **Execution note:** Start with a failing disposal test (mount and dispose five times), because leaks from page-wide behavior are the main risk of the native choice.
- **Patterns to follow:** upstream `e2e/utils/app-direction.fixture.tsx` mount and `packages/desktop/src/renderer/window/router.tsx`.
- **Test scenarios:**
  - Mounting with session `ses_a` renders that session's timeline from a fake server at `/oc/box/api`.
  - Mounting and disposing five times leaves no `oc-theme` style element, no portal root, no OpenCode attribute or style on `html`, no open event stream or WebSocket, and exactly one palette opens on mod+K after a sixth mount.
  - Toggling `dark` on `html` switches the view's color scheme, and a mounted diff and a Mermaid diagram follow it; the light/dark control is absent from settings, and the color-theme picker remains.
  - The mod+K palette dialog and the model select dropdown render inside the portal root with OpenCode's theme variables resolved.
  - No bundled CSS rule selects `:root`, `html`, or `body`; RocketClaw's computed page styles are unchanged with the view mounted.
  - With the language set to Arabic, the container gets `dir="rtl"` and `html.dir` is unchanged.
  - A host-prevented ⌘P keydown does not open any OpenCode command.
  - A keydown on an element outside the view does not reach OpenCode keybinds or composer autofocus.
  - After Escape blurs the composer, mod+K still opens OpenCode's palette, and typing refocuses the composer.
  - Opening settings and pressing its back button returns to the session.
  - Ctrl+` with focus in the terminal toggles the terminal, as upstream does.
  - A file dropped outside the view is ignored; one dropped inside attaches.
  - A container 600px wide inside a 1400px window uses the narrow layout, including the session screen's column direction.
  - Server version `2.2.0` against copy `2.1.3` shows the pill with both versions and the session still renders; `2.1.9` shows no pill.
  - Upstream returns 401: the sign-in notice replaces the view, and no browser auth prompt appears. The server becomes reachable: the session renders without remount.
  - The server becomes unreachable while a draft is typed: the banner shows over the session, and the draft is still there after recovery.
  - Typing a draft, disposing, and remounting restores the draft.
  - A fork action calls the host's open-session callback with the new session ID; deleting the session calls the close callback; the project name click calls nothing.
  - Navigating into a child session calls the report callback with the child's ID.
- **Verification:** Host tests pass, every upstream edit is marked, and RocketClaw's page styles are unchanged with the view mounted, as checked by U6's browser test.

### U4. Same-origin proxy route and removal of the relay

- **Goal:** The browser reaches each server only through `/oc/{server}/api/...` on the Web listener, and nothing of the per-port relay remains.
- **Requirements:** R13, R14, R15, R24; KTD10, KTD11.
- **Dependencies:** U1 (embed directory).
- **Files:**
  - Modify:
    - `internal/rocketclaw/frontend/rpc/http.go` (route registration)
    - `internal/rocketclaw/frontend/rpc/relay.go` (proxy replaces `NewOpenCodeRelay`; delete `AdmitHTTP`)
    - `cmd/rocketclaw/web.go`
    - `cmd/rocketclaw/tailnet.go`
    - `internal/rocketclaw/config/config.go` (remove `Port` and its check)
    - `internal/rocketclaw/rocketclaw.example.json`
    - `cmd/rocketclaw/CHEATSHEET.md`
    - `internal/rocketclaw/frontend/rpc/README.md`
    - every `NewHTTPHandler` call site
  - Test:
    - `internal/rocketclaw/frontend/rpc/relay_test.go` (rewritten for the proxy)
    - `cmd/rocketclaw/web_test.go` (rewrite `TestWebRPCRelaysAndSyncsOpenCode`; delete `TestWebRPCFailsWhenARelayPortIsTaken`)
    - `internal/rocketclaw/config/config_test.go`
- **Approach:**
  1. `NewHTTPHandler` takes the configured servers and registers the proxy per method under `/oc/{server}/api/{path...}` (KTD10). An unknown server name answers 404.
  2. Remove the relay map, relay listeners, and relay servers from `web.go` and `tailnet.go`.
  3. Remove `Port` from `OpenCodeConfig`, its validation, and the example. Do not add rejection of the old field (R15); JSON decoding already ignores unknown fields.
  4. Rewrite the CHEATSHEET `opencode` paragraph: no port and no ACL port note. Keep the full-control warning.
- **Patterns to follow:** `relay.go` `httputil.ReverseProxy` with `Rewrite` and `FlushInterval: -1`; `/pages-raw/` headers in `http.go`; error wrapping per `docs/solutions/best-practices/wrapcheck-cross-package-helper-error.md`.
- **Test scenarios:**
  - An admitted `web_users` caller's `GET /oc/box/api/session` reaches the fake upstream with path `/api/session`, the query kept, Basic auth `opencode:pw`, and no `Cookie`.
  - An unknown IP gets refused before any upstream request.
  - On the tailnet TLS listener, an admitted Tailscale user is proxied; a tagged node is refused.
  - A request with `Origin: https://evil.example` is refused with 403, and so is `Origin: null`; a WebSocket upgrade with a foreign Origin is refused.
  - A same-origin WebSocket upgrade to `/oc/box/api/pty/x/connect` reaches upstream with `Origin` set to the upstream origin, and frames flow both ways.
  - The event stream delivers each event before the upstream response ends, on both listeners.
  - An upstream 401 reaches the browser without `WWW-Authenticate`; an upstream `Set-Cookie` is dropped.
  - Every proxied response carries `Content-Security-Policy: sandbox` and `nosniff`.
  - `/oc/nope/api/x` answers 404; `/oc/box/other` is not routed to the server.
  - A config file still containing `"port": 4100` loads (R15).
  - `startWebRPC` with one configured server serves the proxy on the Web listener and opens no other listener.
- **Verification:** `go test ./...` passes, the Go source line count stays under 25000, coverage stays at or above `COVERAGE_REQUIRED`, and `rg` finds no `NewOpenCodeRelay`, relay listener, or `Port` reference in OpenCode config.

### U5. Server origins to the browser and bi-directional names

- **Goal:** The browser knows each server's origin for the copy-address command, and remote session names live on the server.
- **Requirements:** R18, R19, R20; KTD12, KTD16.
- **Dependencies:** none.
- **Files:**
  - Modify:
    - `internal/rocketclaw/web/proto/web.proto` (`opencode_servers` value type); regenerated `internal/rocketclaw/frontend/rpc/web.pb.go` and protocol files
    - `internal/rocketclaw/frontend/rpc/server.go` (list, update)
    - `internal/rocketclaw/backend/opencode.go` (title update, push of leftover names)
    - the backend's session update path for remote rows
    - `internal/rocketclaw/web/src/{types.ts,session-list.ts}`
  - Test: `internal/rocketclaw/frontend/rpc/opencode_test.go`, `internal/rocketclaw/backend/opencode_test.go`.
- **Approach:**
  1. `ListSessions` fills `opencode_servers` with each server's origin (KTD12).
  2. For a remote session, `UpdateSession` with a name calls the server through the existing `openCodeCall` with `PATCH /api/session/{id}` and `title`, then stores the title. An empty name stays a no-op on the server. A server error surfaces as `Unavailable` and keeps the old title.
  3. `syncOpenCode` pushes each row's leftover RocketClaw-only name as the title, then clears it after a 2xx.
  4. Any query touched follows the CTE rule.
- **Patterns to follow:** `CreateOpenCodeSession` and `openCodeCall` in `internal/rocketclaw/backend/opencode.go`; the `ToggleManualTag` remote branch.
- **Test scenarios:**
  - `ListSessions` returns `opencode_servers` as `{"box": "https://box.example:4096"}` for a configured URL `https://user:secret@box.example:4096/x?auth_token=t`, with no password, userinfo, path, or query anywhere in the response.
  - Naming `opencode:box:ses_a` sends `PATCH /api/session/ses_a` with `{"title": "New"}` and the sidebar row shows "New" without a sync.
  - The server answers 500 to a rename: the RPC fails with `Unavailable` and the row keeps its old title.
  - A row stored with a RocketClaw-only name is pushed as the title on the next sync and its local name is cleared.
  - The server is offline during that sync: the local name stays and is pushed on a later successful sync.
  - A title changed inside OpenCode appears in the row after the next sync.
- **Verification:** Go tests pass, the proto regenerates cleanly, and the Web build type-checks with the new map type.

### U6. Mount the view in RocketClaw Web

- **Goal:** Opening a remote session shows the native view, with RocketClaw's palette, Escape, and tab behavior adapted to it.
- **Requirements:** R1, R5, R6, R8, R9, R11, R12, R18, R21; KTD8, KTD13, KTD17.
- **Dependencies:** U3, U4, U5.
- **Files:**
  - Modify:
    - `internal/rocketclaw/web/src/ui.tsx`: `RemoteSession`, `onKey`, `onEscape`, `useSessionActions`
    - `internal/rocketclaw/web/README.md`
  - Test: `internal/rocketclaw/web/src/opencode.browser.test.ts` (rewritten).
- **Approach:**
  1. `RemoteSession` mounts for any `opencode:` ID whose server is configured (R5). It keeps the offline notice for a server never opened while offline, and the "no longer available" notice for an unconfigured server.
  2. It imports `/oc-ui/` at runtime, calls `mount` in an effect, and calls `dispose` on cleanup. It shows its existing muted "Loading…" status from the start of the import until the first displayed-session report. Remove the frame and its react-doctor suppression.
  3. The host callbacks:
     - Open session: navigates to the session's RocketClaw path in a new tab.
     - New session: goes through the existing `createSession` with `opencodeServer` and `directory`.
     - Close: closes the current tab.
     - Report displayed session: records the session ID the view shows, which "Copy server link" uses.
  4. `onKey` skips events whose target is inside the oc terminal (R11).
  5. `onEscape` returns early on remote session routes (R12).
  6. `useSessionActions` adds two commands for remote sessions:
     - "Copy server link": `copyText` of `<server origin>/server/<base64url-unpadded origin>/session/<displayed session ID>`.
     - "OpenCode commands": calls the mounted view's `openPalette()`.
  7. Keep new branches out of `SessionApp` and `CommandPalette` (react-doctor limit).
- **Patterns to follow:** `CopyPageLink` and `copyText` in `internal/rocketclaw/web/src/transcript-text.tsx`; the `add-tag` gating in `useSessionActions`; `docs/solutions/best-practices/react-doctor-complexity-limit-shapes-web-components.md`.
- **Test scenarios:**
  - Opening `opencode:box:ses_a` mounts the view with no frame and requests `/oc/box/api/...`.
  - Opening a remote session ID absent from the sidebar on a configured server still mounts.
  - Switching to a RocketClaw session and back leaves exactly one mounted view and one open event stream.
  - RocketClaw's page background, fonts, and sidebar styles are identical with and without the view mounted.
  - Pressing Escape twice from the OpenCode composer keeps the tab open, including after clicking timeline text.
  - ⌘P inside the view opens RocketClaw's palette; ⌘K opens OpenCode's.
  - "Copy server link" copies `<origin>/server/<key>/session/ses_a`, with no password.
  - After navigating into a child session, "Copy server link" copies the child's ID.
  - "OpenCode commands" opens OpenCode's palette.
  - The view's fork callback opens a new RocketClaw tab on the forked ID; the delete callback closes the tab.
  - The "Loading…" status appears before the view mounts and is gone after.
  - With vertical tabs, RocketClaw's theme toggle does not overlap OpenCode's review toggle.
- **Verification:** `make -C internal/rocketclaw/web test` passes, the Web TS source stays under 8000, and react-doctor reports no new findings.

---

## Verification Contract

| Gate | Command | Applies to |
|---|---|---|
| Copy builds and unit tests | `make -C internal/rocketclaw/oc build test lint` | U1, U2, U3 |
| Reproducible build output | build twice from a clean install, then `jj diff --git internal/rocketclaw/internal/web/oc-dist` is empty | U1 |
| Go formatting | `gofmt -l` on touched Go files prints nothing | U4, U5 |
| Go tests | `go test ./...` | U4, U5 |
| Web tests | `make -C internal/rocketclaw/web test` | U5, U6 |
| Lint | `make lint` | all |
| Full suite | `make test` | all |
| Line budgets | `make check-cloc-budget`; `make cloc` shows the oc package with copied and `host/` lines separately | all |
| Both listeners | proxy tests run on the plain listener and the tailnet TLS listener | U4 |

## Definition of Done

- Every requirement R1–R25 is met and traced to a passing test or a recorded check.
- The relay, its port field, the frame, and its suppression are gone, and docs describe the same-origin route.
- `internal/rocketclaw/oc/manifest.json` names tag `v2.1.3` and commit `bb25a2f4a3f3`, and lists every copied, renamed, and deleted path.
- Every edit to copied upstream code carries a `rocketclaw:` comment, and `internal/rocketclaw/oc/AGENTS.md` describes the edit and merge rules.
- The committed `oc-dist` matches a fresh build.
- Go and Web TS source counts stay under their budgets; the oc package's count is reported to the user for a budget decision.
- Code from abandoned approaches is removed from the diff.
- The README impact is stated: `internal/rocketclaw/web/README.md`, `cmd/rocketclaw/CHEATSHEET.md`, and `internal/rocketclaw/frontend/rpc/README.md` change; the root README needs none.

---

## Risks

| Risk | Mitigation |
|---|---|
| OpenCode content now renders in RocketClaw's document, so a sanitizer bypass in markdown, Mermaid, SVG, or Office rendering could call RocketClaw's RPCs and every configured server through the proxy, not just one server as the old cross-origin frame allowed. | In-page rendering rests on upstream's sanitizers (DOMPurify, Mermaid's strict mode), kept unedited. The sandbox CSP and `nosniff` headers (KTD10) cover only proxied files opened directly. No service worker is registered. RocketClaw's index page has no document CSP today, and this plan does not add one. |
| Native fences conflict on every upstream merge. | CSS scoping happens at build time, glue lives in `host/`, remaining upstream edits are minimal and marked, and `AGENTS.md` lists them (KTD6, KTD15). |
| Servers upgrade ahead of the copy. | The version pill (R16); the sync loop follows releases. |
| The Go source count sits in its warning zone (24568 of 25000). | Relay removal funds the proxy; stop per the Goal Capsule if a unit would cross 25000. |
| The Web TS count sits in its warning zone (7782 of 8000). | `RemoteSession` shrinks when the frame goes. Solid glue lives in `oc/host/` because it is Solid code (KTD8), and its lines are reported to the user (KTD2); React-side code is never moved there to save Web lines. |
| The committed build adds megabytes per release. | Accepted with R24; the reproducibility check keeps it honest. |
| Upstream's terminal origin check changed on `dev` after v2.1.3. | The proxy's Origin rewrite matches the server's own origin; re-check at each merge. |
