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
  - There was no bounded recovery capability or end-of-turn delete for the full execute output.
  - Path-based recovery granted filesystem read access and sent fetched output through Execute's spill boundary again.
root_cause: missing_size_limit
resolution_type: code_fix
severity: high
tags:
  - execute
  - code-mode
  - spill
  - context-length
  - call-execute
  - result-id
---

# Uncapped Execute Results Exceeded Model Context

## Problem

Code-mode `execute` returned one uncapped string to the model. A large `main()` return hit the provider with `context_length_exceeded`.

The contract is: host tools inside `execute` retain full results; clip only the string `callExecute` sends back to the model; store full oversized output behind a turn-scoped result ID; recover bounded pages through the top-level `load_execute_result` tool; invalidate IDs and delete the turn directory at terminal completion. Restarting the same journaled turn preserves its IDs without changing filesystem permissions. Do not use the session shell-temp tree.

## Symptoms

- Oversized `main()` returns exceeded provider context, regardless of whether their text came from bash, read, MCP, or composition.
- Path-based recovery added exact-file read permission and bound `read` when absent. Returning the fetched file through Execute risked another overflow.
- Host `read` wraps full file content. Recovering a spill through that wrapper coupled output recovery to filesystem access and wrapper-path reuse.

## What Didn't Work

Clipping only inside host tools does not protect the model. Bash/read/glob/grep return full text in Starlark; the model still sees whatever `main()` returns unless `callExecute` clips after `codemode.Run`.

A dual `head_output` / `full_output` object leaves full output easy to return accidentally. Do not bring those attributes back.

Filesystem recovery and wrapper-path reuse were replaced, not retained as a compatibility path. Caller text must never become a storage path. Shell `TMPDIR` carries separate permissions and is not execute overflow storage.

## Solution

`callExecute` clips after `codemode.Run` (`internal/rocketcode/mcp_tools.go`). `clipExecuteHead` preserves the 2000-line / 50 KiB head limit. Small results return unchanged and register nothing. Oversized results write full bytes through `*os.Root`, then register a random ID in the executing looper's `spillResults` map (`internal/rocketcode/execute_results.go`). Storage remains `<spillRel>/<turn-id>/<result-id>.txt`; `Config.SpillDir` placement is unchanged.

The footer names the ID and top-level loader, not the storage path. `mcpToolsFor` assembles `load_execute_result` alongside Execute; it is absent from `CodeModeHosts` and in-script search. Its handler resolves the executing looper from tool-call context. Permission dispatch allows only registered current-turn IDs, without a new permission bucket, filesystem grant, or automatic reviewer.

The loader opens each stored path component relative to the workspace root with `O_NOFOLLOW`, then verifies that the opened file is regular. Checking links before opening would leave a replacement race; applying `O_NOFOLLOW` only to the final component would still follow replaced parent directories. Workspace read/edit tools deny the configured spill tree, and searches exclude its files. Bash blocks explicit paths in arguments, redirections, and `workdir`; indirect paths constructed by scripts or variables remain outside these checks. The loader name is reserved during custom-tool registration so its permission exception cannot authorize a custom handler.

Loader inputs are `result_id`, `start_line`, `limit`, and `line_numbers`. Lines are 1-based; zero start means 1, zero limit means 2000, positive limits cap at 2000, and negatives fail. Pages preserve blank lines, CRLF, and missing final newlines. Responses stay within 50 KiB including numbering and footer, with 1 KiB reserved for markers. Follow `[next_start_line=N]` until `[EOF]`. A line too large for a fresh page returns a UTF-8-safe prefix, explicitly marks omitted bytes, drains the rest, and advances; its omitted tail cannot be fetched through this line-only API. Stored bytes remain unchanged.

`runTurn` begins the result book and defers cleanup (`internal/rocketcode/looper.go`). Registration, rooted file opening, streaming page reads, and cleanup share `spillMu`. Buffered fragments observe cancellation while scanning or draining. Reads deliberately serialize and scan the prefix; indexing or shorter lock scope is a future option only if measured demand requires it. Cleanup invalidates IDs before deleting the turn directory on success, ordinary error, and interrupt. Cancellation of the outer context leaves the journaled turn resumable, as in the existing restart flow. On resume, `restoreTurnExecuteResults` rebuilds only that turn's ID registry from its regular `.txt` files; the loader still rejects symlink components. It never restores read grants or binds a recovery filesystem tool.

## Why This Works

The clipping gate is at the boundary that protects model context, while scripts retain full host content for slicing and searching. Recovery bypasses Execute, so loading cannot spill recursively. ID membership authorizes only one owning turn's registered output, never a caller-supplied path or a sibling turn's result. Existing user filesystem grants and shell-temp permissions keep their separate meaning.

## Prevention

- Keep clipping after `codemode.Run`, not inside host tools or generic dispatch.
- Keep recovery top-level and ID-only; never grant or bind `read` for spilling.
- Keep explicit runtime tool allowlists intact, including allowlists that omit recovery.
- Keep rooted storage and deferred turn cleanup separate from session shell temp; reject symlink substitutions and reserve the loader name.
- Keep exact-output paging tests in `execute_results_test.go`, no-read dispatch coverage in `mcp_tools_test.go`, and real terminal-path cleanup coverage in `looper_test.go`.
- Keep full host-result tests in `bash_starlark_test.go`, `shell_test.go`, and `filesystem_test.go`.

## Related Issues

The earlier execute-output-spill plan remains historical evidence. The replacement contract is `internal/rocketcode/docs/plans/2026-10-05-0854-feat-load-execute-result-plan.md`.
