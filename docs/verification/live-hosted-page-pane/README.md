# Live Hosted Page pane

`make -C docs/verification/live-hosted-page-pane` translates the PlusCal and checks it with `tla2tools.jar` v1.8.0,
SHA-256 `67f9575c3b9ef7a0787b1bded34812862ffe79d711298e5a75b1a77720b19928`.

On 2026-11-26, `make -k` with TLC `2026.11.26.020825` (rev `1138302`) and OpenJDK `21.0.13.1`
matched every TLC outcome (1 pass, 5 named counterexamples) in 36s.

TLC `2026.11.19.180144` (`bc1b506`) on Java `21.0.13.1` explored 315,648 distinct states (2,629,825 generated, depth 26) in 33 seconds. `CurrentSelection`, `LastSuccess`, `NoRedundantMount` and `FreshWhenStable` passed.

| Check | Expected violation |
| --- | --- |
| `stale` applies an obsolete result | `CurrentSelection` |
| `keep-absent` keeps the previous frame after a successful absence | `LastSuccess` |
| `clear-error` clears the previous frame on a transport error | `LastSuccess` |
| `remount` reloads a frame after reading the same revision | `NoRedundantMount` |
| `witness` forbids observing a later save in the same selection | `NeverLoadedUpdate` |

All five checks found their named violations. The witness shows that freshness isn't proved just by never reloading a frame.

The finite world has four view contexts over three pages, two revisions per page, and four navigation transitions. Two contexts share a page to distinguish navigation identity from the page owner's identity. The publisher may change access or connectivity between request, sampling, and delivery. Closing or switching drops the visible frame and rejects old observations, including A–B–A. The reader is weakly fair; the freshness property applies only when selection, access, publication, and responding connectivity stabilize. The model does not promise a real-time deadline or require an offline browser to progress.

`Viewer` maps to the URL-controlled preview and observer lifetime. `Publisher` maps to page storage and the Page Index visibility check. `Reader` maps to the selected page query and the frame key derived from its last successful response. The model checks response ownership, not HTTP cancellation timing or a browser's frame navigation; browser tests verify both the UI behavior and that the existing sandbox remains on the frame.
