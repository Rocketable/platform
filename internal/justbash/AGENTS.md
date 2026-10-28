# AGENTS.md

This package runs an agent's shell commands in [just-bash](https://github.com/vercel-labs/just-bash), a
TypeScript bash emulator, instead of host bash. The root `AGENTS.md` also applies here.

## Layout

- Go code at this level is RocketClaw's side: the gRPC service on a Unix socket (`server.go`), agent
  settings (`settings.go`), network rules (`network.go`), and unpacking the embedded runtime
  (`justbash.go`).
- `proto/justbash.proto` defines the protocol. `justbashpb/` and `wrapper/src/gen/` are generated from it.
- `wrapper/` is the TypeScript program Node runs once per bash call. It holds the pinned just-bash version
  (`package.json`, `bun.lock`) and a marker `go.mod` that keeps `node_modules` out of Go package scans.
- `runtime/` is generated and committed: `wrapper.mjs` plus a pruned production `node_modules`. The
  `rocketclaw` binary embeds it.

## Rules

- This package has no source line-count budget. `make cloc` reports its size only.
- Never edit `runtime/`, `justbashpb/`, or `wrapper/src/gen/` by hand. `make runtime` rebuilds `runtime/`
  (Bun only), and `make generate` rebuilds the protocol code (needs `protoc`). Commit the results with the
  change that caused them; rebuilding again must change nothing.
- Pin just-bash to an exact version in `wrapper/package.json`. Bump it only by changing that version,
  running `bun install` in `wrapper/`, then `make runtime test`.
- Node runs the wrapper; Bun only builds it. just-bash's safety layer fails under Bun.
- Keep just-bash's own package layout in `runtime/node_modules`. Re-bundling just-bash breaks `python3`
  and `sqlite3`, which find their workers and CPython files by path.
- Never use `git`; use `jj`. Temporary files go under the repository root's `.tmp/`.
