---
title: Uncapped Execute Results Exceeded Model Context
date: 2026-08-24
category: docs/solutions/runtime-errors/
module: internal/rocketcode
problem_type: runtime_error
component: execute
symptoms:
  - After code mode execute replaced the top-level bash tool, oversized main() returns triggered context_length_exceeded.
  - Host-tool display caps and bash head_output/full_output lived inside execute, so the full text was easy to send back to the model.
  - There was no turn-scoped spill file, exact-file read grant, or end-of-turn delete for the full execute output.
  - Re-reading a spill by inferring its path from the spill directory could create a new file instead of reusing the booked path.
root_cause: missing_size_limit
resolution_type: code_fix
severity: high
tags:
  - execute
  - code-mode
  - spill
  - context-length
  - call-execute
  - spill-paths
---

# Uncapped Execute Results Exceeded Model Context

## Problem

Code-mode `execute` returned one uncapped string to the model. A large `main()` return hit the provider with `context_length_exceeded`.

The intended contract is: host tools inside `execute` return one full string; clip only the string `callExecute` sends back to the model; spill the full text to a turn-scoped file; grant exact-file `read`; delete that turn directory when `runTurn` exits. Do not use the session tmp tree.

A second failure mode sits on that contract. `return read(spill)` wraps the huge file. Treating that wrapper as a new oversized result would write `output-N+1.txt` (spill-2) that contains the wrapper, not reuse the file this turn already booked.

This lives in the current tree. PR #7 is pending and is not landed.

## Symptoms

- Before the spill gate, oversized `execute` results were sent to the model as one string. `callExecute` still ends in `TextToolResult(out)` (`internal/rocketcode/mcp_tools.go:504`); the current tree clips after `codemode.Run` when a looper is present (`internal/rocketcode/mcp_tools.go:495-504`).
- Session reports and the spill plan describe the older bash clip (2000 lines / 50KB) and a later `head_output` / `full_output` split. That dual payload is not in the current tree. Current tests only show the replacement: `BashResult.String()` is the full `Output` (`internal/rocketcode/shell.go:50-52`), Starlark bash has `error_code` only (`internal/rocketcode/bash_starlark.go:30-40`), and tests assert `full_output` is absent (`internal/rocketcode/bash_starlark_test.go:21-23`, `internal/rocketcode/shell_test.go:148-155`). Returning `full_output` from `main()` is the session failure mode those deletions were meant to close.
- Host `read` of a large file is also uncapped and wrapped. `ReadResult` prefixes the whole file with `<path>…</path>` plus type/content tags (`internal/rocketcode/filesystem.go:135-152`). A 2100-line file is returned in full (`internal/rocketcode/filesystem_test.go:254-261`). `return read(spill)` therefore hands `spillExecuteOutput` a string larger than the original spill.
- There was no turn-scoped spill book. The current book is `looper.spillPaths` (`internal/rocketcode/looper.go:111-116`), reset at turn start (`internal/rocketcode/execute_spill.go:63-68`). Inferring "this is a spill" from a directory prefix would treat any path under the spill dir as reusable, including files this turn did not write.

## What Didn't Work

Clipping only inside host tools does not protect the model. After uncap, bash/read/glob/grep return one full string in Starlark (`internal/rocketcode/shell.go:50-52`, `internal/rocketcode/filesystem.go:155-172`). The model still sees whatever `main()` returns unless `callExecute` clips after `codemode.Run`.

A dual `head_output` / `full_output` object makes the overflow path the default leak. The plan states that split was the opposite of the old bash contract (`internal/rocketcode/docs/plans/2026-08-24-1315-feat-execute-output-spill-plan.md:31-32`). Current code deleted those attrs; do not bring them back.

Writing a new spill for every oversized string fails the `return read(spill)` case. `ReadResult` wraps the file (`internal/rocketcode/filesystem.go:135-152`). That wrapper is oversized (`internal/rocketcode/execute_spill_test.go:90-91`). A naive write would increment `spillSeq` and create `output-3.txt` after `output-1.txt` and `output-2.txt` already exist.

Inferring reuse from a spill-directory prefix is the wrong fix. `existingTurnSpillPath` must not treat "under `.rocketcode/spill/…`" as membership. Only paths this turn wrote belong in the reuse set.

Spilling into the session tmp tree is also wrong. RocketClaw shell temp is `<runtimeDir>/.rocketcode/tmp/<conversation>` (`internal/rocketclaw/harnessbridge/bridge.go:1893-1896`). That tree is `TMPDIR` for bash, not execute overflow. The plan warns it already carries a broader grant (`internal/rocketcode/docs/plans/2026-08-24-1315-feat-execute-output-spill-plan.md:100`, `internal/rocketcode/docs/plans/2026-08-24-1315-feat-execute-output-spill-plan.md:116`).

## Solution

Clip only at the execute-to-model boundary. After `codemode.Run`, `callExecute` calls `tc.looper.spillExecuteOutput(out)` when a looper is on the tool-call context, then returns that string (`internal/rocketcode/mcp_tools.go:495-504`). If that context is missing, the current code still returns the raw string. That path is unspilled.

`spillExecuteOutput` clips with `clipExecuteHead`: 2000 lines or 50KB, whichever hits first (`internal/rocketcode/execute_spill.go:12-14`, `internal/rocketcode/execute_spill.go:18-53`, `internal/rocketcode/execute_spill.go:84-88`). Under both caps, the original string is returned unchanged. Over either cap, the full text is written through `*os.Root` to `<spillRel>/<turn-id>/output-N.txt` (`internal/rocketcode/execute_spill.go:105-116`). Default `spillRel` is `.rocketcode/spill` (`internal/rocketcode/execute_spill.go:15`, `internal/rocketcode/execute_spill.go:156-162`). `Config.SpillDir` empty uses that default; a set value must resolve inside the workspace root (`internal/rocketcode/rocketcode.go:42-44`, `internal/rocketcode/rocketcode.go:277-280`, `internal/rocketcode/execute_spill.go:165-205`). RocketClaw managed turns pass `<workspace>/<runtimeDir>/.rocketcode/spill` (`internal/rocketclaw/harnessbridge/bridge.go:1931-1933`).

The model result is the clipped head plus a footer that names `read(filePath="…")`, says the file dies at end of turn, and says returning the whole file from `main()` spills again (`internal/rocketcode/execute_spill.go:55-61`, `internal/rocketcode/execute_spill.go:130`). Footer copy does not say "call the read tool" (`internal/rocketcode/execute_spill_test.go:69`).

Each new spill file gets an exact-file `read` allow on the live looper (`internal/rocketcode/execute_spill.go:118-120`, `internal/rocketcode/permission.go:214-216`). If `read` is not already a code host, the looper binds `sandboxRead` for the rest of the turn (`internal/rocketcode/execute_spill.go:122-128`). There is no directory, glob, or edit grant in this path. A non-spill path stays unmatched (`internal/rocketcode/execute_spill_test.go:82-84`).

Turn lifetime is `runTurn`: `beginTurnSpills(turnID)` then `defer endTurnSpills()` (`internal/rocketcode/looper.go:652-655`). `turnID` is the turn timestamp nanos (`internal/rocketcode/looper.go:831-833`). `beginTurnSpills` clears `spillPaths` and `spillSeq` (`internal/rocketcode/execute_spill.go:63-68`). `endTurnSpills` deletes `<spillRel>/<turn-id>` through `*os.Root` (`internal/rocketcode/execute_spill.go:71-82`). That is not the session tmp tree.

The re-read trap is a membership check, not a directory check. Before writing, `spillExecuteOutput` parses a leading `<path>…</path>` from the execute result (`internal/rocketcode/execute_spill.go:101-103`, `internal/rocketcode/execute_spill.go:133-145`). `existingTurnSpillPath` cleans that path and returns it only when `slices.Contains(l.spillPaths, rel)` (`internal/rocketcode/execute_spill.go:147-154`). On hit, it returns head plus footer for that existing path and does not increment `spillSeq`, write a file, or append `spillPaths`. A second distinct oversized string still gets `output-2.txt`. Re-spilling the `ReadResult` wrapper of `output-1.txt` keeps `spillPaths` at those two files and does not create `output-3.txt` (`internal/rocketcode/execute_spill_test.go:86-101`).

`codeModeSystemPrompt` currently has no spill catalog line (`internal/rocketcode/mcp_tools.go:162-218`). The footer is the in-band teaching surface in this tree.

## Why This Works

The model context only sees the `callExecute` return. Uncapping host tools inside Starlark is required so the script can slice or search. The size gate belongs after `codemode.Run`, not in bash/read/glob/grep and not in `dispatchToolCalls` (that path also carries `task`, `skill`, and custom tools).

`spillPaths` is the set of files this turn wrote. `ReadResult` always starts with `<path>` plus the requested filename (`internal/rocketcode/filesystem.go:136-138`), so `readResultPath` can recover that filename without walking a directory. Membership in `spillPaths` is the reuse predicate. A prefix test would reuse or collide with files this turn did not book.

Reusing the booked path avoids wrapping a wrapper. The file on disk stays the original execute output (`internal/rocketcode/execute_spill_test.go:71-73`). The model still gets a clipped head of the wrapper plus a footer that points at `output-1.txt`, not a new nested spill.

Deleting `<spillRel>/<turn-id>` on `runTurn` exit is local to RocketCode. It does not depend on RocketClaw `ClearCompletedTurn`. `defer endTurnSpills()` runs on success, error, and interrupt for that function. Grants are live-looper appends; they do not survive as a way to read deleted bytes once the file is gone. RocketClaw builds a new looper per managed turn; leftover grants on a reused standalone `Loop` would hit a missing file.

## Prevention

Keep the gate in `callExecute` after `codemode.Run` (`internal/rocketcode/mcp_tools.go:495-504`). Do not reintroduce host-tool display caps or bash `full_output` / `head_output`.

Keep reuse as `readResultPath` plus `spillPaths` membership (`internal/rocketcode/execute_spill.go:101-103`, `internal/rocketcode/execute_spill.go:133-154`). Do not infer from a directory prefix. Do not write spills under `<runtimeDir>/.rocketcode/tmp/…`.

Keep turn cleanup as `defer l.endTurnSpills()` next to `beginTurnSpills` in `runTurn` (`internal/rocketcode/looper.go:654-655`). Write and delete through `*os.Root`. Tests must create fixtures the same way.

Keep these tests. They are the contract:

- `TestClipExecuteHead` (`internal/rocketcode/execute_spill_test.go:12-36`): under-cap text is unchanged; 2100 lines clips to 2000 newlines; a byte-cap string is `<= 50KB`; the clip helper itself does not inject the footer.
- `TestSpillExecuteOutput` small path (`internal/rocketcode/execute_spill_test.go:55-57`): `"ok"` returns unchanged, no file.
- `TestSpillExecuteOutput` first overflow (`internal/rocketcode/execute_spill_test.go:64-84`): footer contains `read(filePath=".rocketcode/spill/turn-1/output-1.txt")` and the end-of-turn sentence; file bytes equal the full input; exact-file `read` is allowed; `read` is bound as a host; `secret.txt` is not allowed.
- `TestSpillExecuteOutput` second overflow (`internal/rocketcode/execute_spill_test.go:86-88`): a new oversized string books `output-2.txt`.
- `TestSpillExecuteOutput` read-wrapper reuse (`internal/rocketcode/execute_spill_test.go:90-101`): `ReadResult` of `output-1.txt` is oversized; re-spill footers `output-1.txt`, does not mention `output-3.txt`, leaves `spillPaths` as those two files, and does not create `output-3.txt`.
- `TestSpillExecuteOutput` turn delete (`internal/rocketcode/execute_spill_test.go:103-106`): `endTurnSpills` removes the turn file.
- `TestSpillExecuteOutputWriteFailure` (`internal/rocketcode/execute_spill_test.go:109-125`): a blocked spill dir errors without echoing the huge payload.
- `TestBashStarlarkResult` (`internal/rocketcode/bash_starlark_test.go:10-29`): Starlark bash string is the full output; `full_output` is missing; `error_code` remains.
- Shell "returns full multi-line output" (`internal/rocketcode/shell_test.go:148-155`): 2100 echoed lines are all present; no `full_output`; no execute-boundary footer inside bash.
- Filesystem many-lines / offset / long-line (`internal/rocketcode/filesystem_test.go:254-276`): `read` of 2100 lines includes line 2100; `offset=2001` starts at 2001; an 80KB line is not char-trimmed.

Add a test if reuse logic changes: a path under the spill directory that is not in `spillPaths` must write a new file, not reuse. The current suite does not assert that negative case.

PR #7 is pending. Do not treat this contract as merged until it lands.

## Related Issues

No related durable learnings in `docs/solutions/`. GitHub issues are disabled on Rocketable/platform. PR #7 is pending and is not landed.
