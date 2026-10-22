# AGENTS.md

This package is a copy of OpenCode's web UI (`https://github.com/anomalyco/opencode`), kept mergeable with
upstream's tagged v2 releases. The root `AGENTS.md` also applies here.

## Layout

- `packages/`, `patches/`, and `LICENSE` hold copied upstream files at their upstream paths.
  `manifest.json` lists them.
- `host/` holds RocketClaw's own glue code. `make cloc` counts it apart from the copied code. Copied code
  imports it as `@rocketclaw/host/<file>`, a `tsconfig.json` path.
- RocketClaw owns these files, even where upstream has a file at the same path: `AGENTS.md`,
  `manifest.json`, `Makefile`, `go.mod`, `.gitignore`, `package.json`, `bun.lock`, `tsconfig.json`, and
  `vite.config.ts`. Upstream's per-package `tsconfig.json` and `bunfig.toml` files are not copied. The root
  `tsconfig.json` and the `Makefile` test flags replace them.
- The built UI goes to `../internal/web/oc-dist/` and is committed. Rebuild it with `make build` after every
  change here, and commit it with the change.

## Editing copied upstream code

- Put new behavior in `host/` when it can live there. Edit a copied file only when the change cannot be
  made from outside it.
- Some fences need no edit in copied files: `vite.config.ts` scopes every bundled stylesheet to the view
  (`host/scope-css.ts`), and `host/overrides.ts` replaces `solid-js/web`'s `Portal` and
  `@solid-primitives/media`'s `createMediaQuery` for every importer. Code a new release adds is covered
  without edits.
- `host/defaults.ts` sets RocketClaw's defaults for settings nobody changed: Show agent on, terminal at the
  bottom, follow-ups queued, and timeline detail at Everything. It also sets them once in each browser's saved
  settings, since saving one setting saves all of them. Keep them through every merge; a merge that renames
  one of these settings, or the `settings.v3` storage key, updates `host/defaults.ts` to match.
- Keep each edit minimal and local. Mark each edit with a `rocketclaw:` comment that says why the edit
  exists and cites the file's upstream path, for example
  `// rocketclaw: <why> (upstream packages/app/src/foo.tsx)`.
- Never rename or move a copied file, and never change an `@opencode/*` package specifier. The specifiers
  resolve to `packages/` through `tsconfig.json` `paths`, which mirror each copied `package.json`'s
  `exports`; `vite.config.ts` turns them into aliases. When a merge changes a package's `exports`, update
  the `paths` to match.
- The one exception is file names that contain "opencode": no RocketClaw file name may contain it, except
  the provider icons the code looks up by provider ID. Such a file is renamed once, and `renamed` in the
  manifest maps its upstream path to the copy's path.
- Keep `manifest.json` current: add each newly copied upstream path to `copied`, record each rename in
  `renamed` (upstream path → copy path), and move each upstream path the copy deletes to `deleted`. A path
  in `deleted` is never copied again; one ending in `/` covers its whole directory.
- Third-party libraries stay npm dependencies at the exact versions in upstream's `bun.lock`. Where
  upstream pins two versions of one library, use `packages/app`'s. `patches/` holds upstream's patches for
  the libraries the UI uses, declared in `package.json` `patchedDependencies`.

## Merging a new upstream release

Upstream's release tags are side commits off its `dev` branch, so the merge base is the old tag's tree,
never tag ancestry. Read upstream only at tags, with `jj file show -r <tag> <path>` in the upstream
checkout.

1. For each path in `copied` (at its `renamed` path when renamed), three-way merge the old tag's file
   (base), the new tag's file, and the copy. Conflicts land on RocketClaw's marked edits; keep each edit
   marked and minimal.
2. A path the new tag deletes upstream is deleted from the copy and from `copied`.
3. A file the new tag adds is copied only when the UI reaches it from `host/entry.ts`, the build's entry, or
   when it is a unit test of a copied module; skip paths in `deleted`.
4. Update `package.json` versions and `patches/` from the new tag's `bun.lock` and patches, run
   `bun install`, and update `tsconfig.json` `paths` when `exports` changed.
5. Set `tag` and `commit` in the manifest, then run `make lint test build` and commit the rebuilt
   `oc-dist`.

## Rules

- Never use `git`; use `jj`.
- Put temporary files, scratch exports of upstream trees, and clones only under the repository root's
  `.tmp/`.
