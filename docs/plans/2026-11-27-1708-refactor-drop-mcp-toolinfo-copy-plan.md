---
title: Drop the MCP Tool Copy and the Unreachable Marshal Error - Plan
type: refactor
date: 2026-11-27
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Drop the MCP Tool Copy and the Unreachable Marshal Error - Plan

## Goal Capsule

- **Objective:** code mode and the model see exactly the same MCP tool names, descriptions, input schemas and tool results as today, while the MCP client carries less code.
- **Means:** `Session.ListTools` returns the library's tool values and the harness builds `codemode.ToolDesc` from them (KTD1). `flattenCallToolResult` drops the marshal error branch that decoded data cannot reach (KTD2).
- **Authority:** the owner's decision in the invoking request, then this plan, then the repository's `AGENTS.md`.
- **Execution profile:** one PR, two small units, no behavior change.
- **Stop conditions:** stop and report if source CLOC does not go down from 45269, or if any tool name, description, schema or result text reaching code mode would change.
- **Finishing:** the implementing agent builds, verifies, and opens the PR. The owner merges.

---

## Product Contract

### Summary

Remove `mcpclient.ToolInfo`, a struct that copies three fields of `mcp.Tool` only for the harness to copy them again into `codemode.ToolDesc`. Remove the marshal error handling for structured tool results, which cannot fail for data the MCP library decodes from the wire.

### Problem Frame

Listing MCP tools copies each tool twice on its way to code mode, and the middle copy adds a type and a loop body that do nothing. The tool result flattener has an error branch, with its own nested error case, for a marshal that only fails on Go values that JSON decoding never produces. Both cost lines and reader attention.

### Requirements

**Tool listing**
- R1. Code mode receives the same server, name, description and input schema for every allowed MCP tool. A tool whose input schema is not a JSON object still gets a nil schema, as today.
- R2. `mcpclient.ToolInfo` no longer exists.

**Tool results**
- R3. `CallTool` returns the same text and errors for every result an MCP server can send: concatenated text content first, structured content as JSON when there is no text, and an error carrying that text (or "mcp tool error") when the server flags the result as an error.

### Key Decisions

- **Remove the middle copy with no behavior change.** (session-settled: user-approved — chosen over keeping `ToolInfo`: it duplicates `mcp.Tool` for a single caller.) Governs R1, R2.
- **Remove the structured content marshal error branch only because it is provably unreachable.** (session-settled: user-approved — chosen over keeping the branch: it guards a failure that decoded JSON cannot produce.) Governs R3.

### Scope Boundaries

- Not changing `internal/rocketclaw/harness/mcp_result.go`, which has a similar marshal error branch for the same structured content. The decision names only `registry.go`; this is reported as a possible follow-up.
- Not changing `flattenCallToolResult`'s signature. It still returns an error for results the server flags as errors.

---

## Planning Contract

### Key Technical Decisions

- KTD1. **`ListTools` returns `[]*mcp.Tool`, and `listAllowedTools` in `internal/rocketclaw/harness/mcp_tools.go` keeps the `schema, _ := tool.InputSchema.(map[string]any)` assertion.** This is the only production caller. The assertion moves rather than changes, so a non-object schema still becomes nil (R1). Returning the library type adds no new type and no exported symbol. (session-settled: user-approved — chosen over keeping `ToolInfo`: it is a parallel mirror of `mcp.Tool`.)
- KTD2. **Ignore the `json.Marshal` error on `result.StructuredContent`.** The value always comes from the wire, so it is one of `map[string]any`, `[]any`, `string`, `float64`, `bool` or nil, and `encoding/json` cannot fail on those. The evidence, from the vendored go-sdk v1.8.0 and segmentio/encoding v0.5.4:
  1. `Session.CallTool` gets results only from `ClientSession.CallTool`, over the command or streamable HTTP transport, with no client middleware installed. The result is decoded by `jsonrpc2.AsyncCall.Await` into a fresh `*CallToolResult` through `internal/json.Unmarshal`, and `CallToolResult.UnmarshalJSON` decodes into a fresh wire struct, so `StructuredContent` starts nil.
  2. segmentio's `decodeInterface` with a nil destination only produces `map[string]any`, `[]any`, `string`, `bool`, nil, or a number from `decodeDynamicNumber`. The SDK's decoder sets none of `UseNumber`, `UseInt64`, `UseUint64` or `UseBigInt`, so numbers become `float64`. Nested map and slice values go through the same `decodeInterface` with nil destinations.
  3. `decodeFloat64` returns a decode error when `strconv.ParseFloat` fails, including out-of-range values such as `1e400`, so no `±Inf` or NaN reaches the result.
  4. `encoding/json.Marshal` succeeds on all of these: finite floats encode, invalid UTF-8 in strings is replaced rather than rejected, decoded values have no cycles, and the SDK caps nesting at 1000 levels before decoding.

  (session-settled: user-approved — chosen over keeping the branch: it cannot trigger.)

### Assumptions

- The `TestFlattenCallToolResult` cases that put a Go `chan int` in `StructuredContent` test only the removed branch, or repeat "text wins over structured". They are deleted rather than rewritten.

---

## Implementation Units

### U1. Return library tools from ListTools

- **Goal:** R1, R2.
- **Requirements:** R1, R2, KTD1.
- **Dependencies:** none.
- **Files:** `internal/rocketclaw/mcpclient/registry.go`, `internal/rocketclaw/harness/mcp_tools.go`.
- **Approach:**
  1. Delete `ToolInfo` and have `ListTools` append each `*mcp.Tool` from `cs.Tools` as it is.
  2. In `listAllowedTools`, read `Name` and `Description` from the tool and keep the map assertion for `InputSchema`.
- **Test scenarios:**
  - The existing `TestHTTPCallTool` and `TestStdioCallTool` keep proving that listed tools carry their names and object schemas.
  - The existing `TestSessionErrors` and `TestListToolsError` keep proving that a failed listing returns nil tools and the same error text.
  - The existing harness MCP tests keep proving what code mode receives.
- **Verification:** `rg ToolInfo internal/rocketclaw` finds nothing.

### U2. Drop the unreachable marshal error

- **Goal:** R3.
- **Requirements:** R3, KTD2.
- **Dependencies:** none.
- **Files:** `internal/rocketclaw/mcpclient/registry.go`, `internal/rocketclaw/mcpclient/registry_test.go`.
- **Approach:**
  1. Marshal structured content without checking the error, and use `result.IsError` directly.
  2. Delete the `chan int` test cases.
- **Test scenarios:**
  - The remaining `TestFlattenCallToolResult` cases keep covering text concatenation, structured-only results, text over structured, empty success, and error results with and without structured content.
  - `TestHTTPCallTool` keeps covering error results and structured results through a real server.
- **Verification:** `rg "marshal structured content" internal/rocketclaw/mcpclient` finds nothing.

---

## Verification Contract

| Gate | Command |
|---|---|
| Format | `gofmt -l` on touched files prints nothing |
| Lint | `make -C internal/rocketclaw lint` |
| Tests | `make -C internal/rocketclaw coverage-profile GOTESTPKGS="./mcpclient ./harness"` |
| Size | `make -C internal/rocketclaw check-cloc-budget`, with source CLOC below the 45269 baseline |

## Definition of Done

- U1 and U2 land in one commit in one PR, with every gate above passing.
- Non-test source lines go down from 45269.
- R1 to R3 are rechecked against the diff.
- No leftover code from approaches that were tried and abandoned.
