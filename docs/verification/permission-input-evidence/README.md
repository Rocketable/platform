# Permission-review input evidence

The state diagram, code mapping and bounds are in
[`2026-11-17-permission-input-evidence.md`](../../plans/2026-11-17-permission-input-evidence.md).

On 2026-11-17, the model was translated and all five checks ran with:

```sh
make -C docs/verification/permission-input-evidence JAVA_BIN="$PWD/.tmp/java"
```

The local Java launcher runs Eclipse Temurin 21.0.13 in a container, with the
repository mounted at its existing path, preserving the working directory and
putting Java scratch under the repository's `.tmp/`. Native Java works without
the override.

The tool file has the required SHA-256
`e1f11b75c4b5bc4478ba0ac5cb6ae28e40a01b0016bc33e0aa97da22dfdbcd7f`;
it reports TLC `2026.11.06.222357`, revision `d8fb231`, and PlusCal translator
`1.12`. The download uses the official editor-extension artifact containing that
exact tool, and verifies both the archive and the extracted JAR. All tool files,
logs and model-checker state are under
`.tmp/permission-input-evidence-model/`.

| Check | Outcome | Generated / distinct states | Depth |
| --- | --- | --- | --- |
| pass | No error found | 877 / 252 | 10 |
| compact-loss | Expected `OrderedEvidence` violation | 8 / 8 | 3 |
| restart-loss | Expected `OrderedEvidence` violation | 9 / 9 | 3 |
| aliased-snapshot | Expected `SnapshotStable` violation | 22 / 16 | 3 |
| turn-isolation | Expected `TurnIsolation` violation | 11 / 11 | 3 |

Each TLC run finished in under one second. The positive check exhausted its queue.
Negative checks required exit status 12 or 13 and the named invariant violation.
The first translation attempt used an incorrect container working directory and
failed before translation; it was corrected before these results.

The finite model treats incorporation and its journal save as one atomic
transition and assumes immutable event identities. It does not explore a crash
between in-memory incorporation and saving. It
checks preservation and order, not whether an input genuinely authorizes an
action. It does not prove model judgment, attachment handling, database atomicity,
the Go implementation or retroactive recovery of evidence already discarded by
an older checkpoint. No liveness or fairness claim is made.
