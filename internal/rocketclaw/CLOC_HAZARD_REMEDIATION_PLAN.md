# CLOC Hazard Remediation Plan

Goal: reduce source CLOC by at least 180 lines, behavior-neutral, with no ADR meaning changes.

Current source CLOC: 17652.
Hazard threshold: 17550.
Preferred target: 17450 or lower.

## 1. Shared Primary Text Helpers

Create one small shared primary-text helper package, likely `internal/rocketclaw/primarytext`.

Include:

- Shared response chunk splitter with connector-specific limits and boundary mode.
- Shared thread-agent normalization and prefix matching.

Splitter shape requirement:

- Do not pass a boolean such as `hardBoundary` into the shared splitter. That obscures connector behavior at the call site.
- Use connector-specific chunk-end functions instead:

```go
func splitText(text string, preferredLimit, hardLimit int, chunkEnd func([]rune, int, int) int) []string

func SplitSlackText(text string, preferredLimit, hardLimit int) []string {
	return splitText(text, preferredLimit, hardLimit, slackChunkEnd)
}

func SplitDiscordText(text string, preferredLimit, hardLimit int) []string {
	return splitText(text, preferredLimit, hardLimit, discordChunkEnd)
}
```

- `slackChunkEnd` must check preferred-limit Slack boundaries, then hard-limit Slack boundaries, then fall back to hard limit.
- `discordChunkEnd` must check preferred-limit Discord boundaries, then fall back directly to hard limit.

Expected net savings: 60 to 90 source lines.

Why first:

- Safest substantial reduction.
- Aligns with ADR 0001 and ADR 0002 primary text parity requirements.
- Keeps Slack and Discord transport behavior in connector packages.

## 2. Replace Slack And Discord Response Chunking

Files:

- `internal/rocketclaw/slackconnector/connector.go`
- `internal/rocketclaw/discordtext/connector.go`

Implementation:

- Replace `splitSlackResponseText` body with a shared helper call.
- Delete `slackChunkEnd`, `slackChunkBoundary`, and `lastSlackChunkBoundary`.
- Replace `splitDiscordResponseText` body with a shared helper call.
- Delete `discordChunkEnd` and `lastDiscordChunkBoundary`.

Preserve exact current behavior:

- Slack: preferred 3200, hard 3800, boundaries: blank line, newline, whitespace.
- Discord: preferred 1700, hard 1900, boundary: whitespace only, fallback directly to hard limit.
- Avoid boolean parameters in this shared splitter path; Slack and Discord hard-limit retry behavior must be explicit in `slackChunkEnd` and `discordChunkEnd`.

Expected net savings: 45 to 65 source lines.

## 3. Replace Slack And Discord Thread-Agent Matching

Files:

- `internal/rocketclaw/slackconnector/connector.go`
- `internal/rocketclaw/discordtext/connector.go`

Implementation:

- Replace local `threadAgent` with shared `primarytext.ThreadAgent`.
- Replace duplicated `normalizeThreadAgents` with a shared function.
- Replace Slack `threadAgentForText` and Discord `threadAgentPrompt` with a shared matching call or thin wrapper.
- Preserve Slack longest-prefix ordering.

Decision before implementation:

- Preserve Discord's current lexical-desc sort exactly for strict behavior neutrality, or align Discord to Slack longest-prefix parity because ADRs require Slack/Discord parity and the current difference is likely accidental.

Expected net savings: 25 to 45 source lines.

## 4. Add App-Level Primary Text Relay Adapter

File:

- `internal/rocketclaw/app/app.go`

Implementation:

- Introduce an app-local struct with function fields for response sending, voice relay, external MCP root relay, external MCP thread relay, and pending-relay cleanup.
- Build exactly one adapter after Slack or Discord connector startup.
- Replace `primaryTextSend`, `relayPrimaryTextVoice`, `textRelay`, and `cleanupTextRelay` parallel branches with adapter calls.

Expected net savings: 30 to 55 source lines.

Risk:

- Medium because relay routing is product-visible.
- Preserve error wrapping and reply-target construction where possible.

## 5. Remove Tiny Wrappers And Helpers

Files:

- `internal/rocketclaw/externalmcp/server.go`
- `internal/rocketclaw/webui/server.go`
- `internal/rocketclaw/slackconnector/connector.go`
- Optionally `cmd/rocketclaw/fc.go`

Implementation:

- Remove one-line `Stop(ctx) { return Close(ctx) }` wrappers and register `Close` directly in app stop list.
- Inline `limitedBuffer.Bytes()`.
- Only inline command helpers if it clearly reduces lines without making CLI dispatch harder to read.

Expected net savings: 10 to 20 source lines.

## 6. Verification

Run after edits:

- `gofmt` on touched Go files.
- `make test`
- `make cloc`
- `make check-cloc-budget`

Success criteria:

- Source CLOC at or below 17472 as the minimum acceptable target.
- Prefer source CLOC at or below 17450.
- No ADR edits.
- No behavior changes to message order, connector routing, chunk text, cron relay, stop behavior, or cleanup behavior.
