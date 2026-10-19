# State Store equivalence harness

This harness checks that the State Store refactor changes no read people or agents see. It takes
one copy of a real State Store database, reads it once with the code from before the refactor
(the base revision) and once with the code in this tree, and compares the two results.

## What it proves

Both trees read the same data through the same functions Web, Slack and the agent tools use:
the sidebar, History and its pages, search, origin, undo, the queue, Background Jobs, goals,
reminders, cron, External MCP sessions and the session tools. Each read is saved as one line per
key in a capture file. The report then lists, for every read, how many keys match, how many
differ, and whether each difference is a documented, intended change. A read passes when every
difference is intended. If any read has a difference nobody explained, the report says FAIL for it.

The after copy starts this tree's code on the before schema, so its startup runs this tree's
migrations. The report records how long they took.

Before any capture, the drain step uses the base tree's own code to finish what a stopped
process leaves behind: running turns, Background Jobs and pending shadow turns. A real
deployment does the same before the downtime. Without it, migration 049 refuses to run.

## Privacy

Captures and the report hold IDs, counts, kinds, flags, timestamps and order. Every message
text, prompt, name and email is a short hash. Even so, conversation IDs and hashes come from real
data, so the outputs stay under the repository's `.tmp/`, readable only by you (the runner sets
`umask 077`). Do not commit or share them.

## How to run it

You need `jj`, `go`, `psql`, and a local Postgres that holds the restored dump in its own
database. The run never changes that database: it works on clones of it.

```sh
internal/rocketclaw/backend/testdata/equivalence/run.sh <restored-database> [postgres-base-url] [base-revision]
```

- `postgres-base-url` defaults to `postgres://postgres:postgres@127.0.0.1:5432`.
- `base-revision` defaults to `heads(::main@origin & ::@)`, the commit this branch is based on.
- `SEARCH_PATH=<schema>` names the State Store's schema when it is not the role's default.
- `OUT=<dir>` sets the output directory (default `.tmp/equivalence/run-<restored-database>`).
- `KEEP=1` keeps the three scratch databases (`<db>_equiv_drained`, `_equiv_before`,
  `_equiv_after`). Without it, they are dropped at the end.

The steps, each timed in `$OUT/run.log`:

1. Create, or reuse, a jj workspace of the base revision at `.tmp/equivalence/base-<commit>`,
   and copy the before files into its `internal/rocketclaw/backend`. Then check that they build.
2. Clone the restored database, and drain the clone with the base tree.
3. Clone the drained copy twice.
4. Capture the first clone with the base tree (`before.jsonl`).
5. Capture the second clone with this tree (`after.jsonl`). Its startup migrates it.
6. Compare the two captures into `report.md`, and print its summary table.

## Trying it on the small fixture

`FIXTURE=1` first creates `<restored-database>`, which must not exist yet. It fills it with a
small State Store that the base tree's own code builds: chats, a Slack thread, cron runs, an
External MCP binding, a shadowed chat, undo, pins, tags, reminders, a goal, remote OpenCode
sessions, Background Jobs, queued work, and some unfinished work for the drain.

```sh
FIXTURE=1 internal/rocketclaw/backend/testdata/equivalence/run.sh equiv_fixture <postgres-base-url>
```

Drop `equiv_fixture` before building it again.

## The files

The capture and the report live in `../../equivalence_test.go` and run in this tree. The base
tree needs its own copy of that file, and these files:

- `equivalence_adapter_test.go.txt`: the base tree's tree adapter, the part that differs between
  the trees (for example, its table names).
- `equivalence_drain_test.go.txt`: the drain.
- `equivalence_fixture_test.go.txt`: the fixture builder.

The runner builds the base tree's `equivalence_test.go` from this tree's file: it keeps
everything above the `// ---- Tree adapter:` line and drops the rest. That way, both trees always
run the same capture code. If that code uses something the base tree does not have, the build
check in step 1 fails, and the adapter has to absorb the difference.

Go skips `testdata` directories, so none of these files build or count in this tree.
