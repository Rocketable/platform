# Shadow comparison: Web evidence

Authority: `docs/plans/2026-10-11-1632-feat-agent-shadow-comparison-plan.md`, U6–U8.
The comparison UI is implemented; this record supersedes the initial grounding
notes that described it as preparatory work.

## Transcript and transport contracts

Comparison grouping uses producer-scoped entry keys and durable outer-turn IDs.
Steers remain in their owning row and consumed inputs render in order, once.
Ordinary transcripts retain their grouping and legacy user-message fallback.
Pagination merges comparison metadata without dropping the visible roster.
Canceled child reads cannot apply stale results or become history errors.

Only the parent's existing stream refreshes comparison metadata. The selected
child uses bounded History reads; inactive children have no streams or polling.
Child/delegation reads inherit explicit parent visibility, including private
producers' recorded destination. Public child mutations are denied.

## Browser checks

`src/shadows.browser.test.ts` checks a shared CSS grid and one scroller at 1440px,
plus the comparison sheet and turn navigation at 390px. Header alignment stays
within one pixel across unequal output heights, missing child turns and delayed
images. Tabs support keyboard navigation; panel width reuses existing resizing.
Delegation/skill modes replace comparison, invalid tabs fall back to a valid
binding, and ordinary parent actions remain available.

`TestShadowsRealBackendBrowser` runs the built SPA through real HTTP/gRPC,
PostgreSQL and a gated provider. Two parent turns finish while two shadows are
held. Releasing only one child updates its two rows without changing the parent
text, proves completed status and keyboard tab selection, and preserves durable
row keys and one-pixel alignment. There is exactly one parent stream, no inactive
child reads, no held-child polling, and direct child prompting returns HTTP 403.
The Go fixture additionally checks identical consumed provider inputs,
independent child histories and parent-only outbound events.

Evidence: `.tmp/rocketclaw/shadow-ui-final-test.log` (169 Bun tests, 2,961 assertions,
real RPC browser fixtures) and `.tmp/rocketclaw/shadow-final-real-u8.log` (race
detector, 28 browser assertions against final assets).
The latter is proof-first evidence, not a claimed pre-implementation failure.
Final full verification remains recorded in `agent-shadow-comparison.md`.

`shadow-refresh.browser.test.ts` also checks delayed initial/cached child reads
and a loaded older comparison completing while the parent tail stays unchanged.
All three cases pass without switching tabs, adding a child stream, reading an
inactive child, or polling. Independent follow-up reviewed both refresh fixes
and confirmed the final source/asset fingerprints.

README impact considered: root and Web guidance needed updates and was updated.

## Mobile controls follow-up (2026-10-12)

The mobile **Shadows** button now shares the source-filter row above the composer.
Source toggles remain independent; without sandboxed messages, only **Shadows**
appears. Shadow rows show **Turn N**, with short tab labels when all bindings share
a parent. Full accessible names and mixed-parent tab labels remain available.

`shadows.browser.test.ts` covers 1440px, 390px, and 320px, source-filter independence,
control placement, tab labels, mixed parents, disappearing shadows, and returning
to the matching parent turn. Desktop alignment remains within one pixel.
Screenshots: `.tmp/rocketclaw/shadow-controls-{320,390}.png` and
`.tmp/rocketclaw/shadow-comparison-{320,390,1440}.png`.

Web `make lint test` passed: 170 Bun tests, 3,005 assertions, the five selected
Go-backed browser/transport fixtures, and the source CLOC budget. No Go source
changed in this follow-up. The Web README was updated to describe the new control.
