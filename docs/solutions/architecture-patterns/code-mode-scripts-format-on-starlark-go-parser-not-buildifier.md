---
title: Code Mode Scripts Are Formatted on starlark-go's Parser, Not by Buildifier
date: 2026-11-18
category: docs/solutions/architecture-patterns/
module: internal/rocketclaw/codemode
problem_type: architecture_pattern
component: execute
severity: medium
applies_when:
  - Changing how Web shows the Starlark source of an Execute call
  - Proposing buildifier (github.com/bazelbuild/buildtools/build) or another Starlark formatter in place of codemode.Format
  - Changing the parse options Code Mode runs scripts with
  - Adding syntax support to internal/rocketclaw/codemode/format.go
related_components:
  - internal/rocketclaw/frontend/rpc
  - internal/rocketclaw/web
tags:
  - code-mode
  - starlark
  - buildifier
  - formatter
  - pretty-printer
  - fuzzing
  - execute
---

# Code Mode Scripts Are Formatted on starlark-go's Parser, Not by Buildifier

## Context

Web shows each Execute call's Starlark script. Models often write it squeezed onto a few long lines, so the script is laid out for reading before Web shows it. `go.starlark.net` has a parser but no printer, so there were two choices: buildifier's formatter (`github.com/bazelbuild/buildtools/build`, the usual Starlark formatter) or a small printer of our own on `go.starlark.net/syntax`.

Buildifier was stress-tested first and rejected. The test harness was a scratch Go module outside the repo. It used buildtools `v0.0.0-20260904073137-eaa4d125b423` and the starlark-go version in `go.mod` (`v0.0.0-20261021141710-4f4267fb140d`). It ran 48 hand-written edge cases, every `def main` script found in the repo's tests and docs, very large and deeply nested inputs, and a fuzzer. Each input was checked four ways: the output must still parse in starlark-go, mean the same program node for node, keep every comment, and not change when formatted again.

## Guidance

Keep formatting in `codemode.Format` (`internal/rocketclaw/codemode/format.go:33`). Do not replace it with buildifier. These are the results that ruled buildifier out:

| Problem | Effect on Code Mode |
|---|---|
| It rewrites single-quoted strings into double quotes, unless they contain a double quote, and writes their non-ASCII bytes as octal escapes. `x = 'é'` becomes `x = "\303\251"`, and `'''日本'''` and `'\u00e9'` go the same way. starlark-go rejects these escapes (`non-ASCII octal escape \303`). Strings already in double quotes come through unchanged, and raw strings keep their text. | Common. PR bodies, messages and file text contain `é`, `—`, CJK and emoji, so the formatted script would no longer parse. |
| Its parser has no `while`. It also rejects bytes literals (`b"..."`) and tab indentation. | Code Mode allows all three (`While: true` in `sourceOptions`, `internal/rocketclaw/codemode/codemode.go:19`), and a repo test script uses `while True` (`internal/rocketclaw/codemode/codemode_test.go:278`). Those scripts could not be formatted. |
| `build.Format` also runs rewrites: it moves keyword arguments ahead of `*args` and sorts `load` names. | The output is a different program: with the arguments moved, tool calls can run in a different order. Only `FormatWithoutRewriting` kept meaning. |
| It does not wrap long lines, and it respaces `key=value` as `key = value`. | A 200-character call stays 200 characters, and long one-line calls were the readability problem to fix. |
| It is quadratic on long `a + b + c` chains. Measured: 20,000 terms (80 KB) took 2.8 s and 100,000 terms (400 KB) took about 2 minutes. | starlark-go parses such scripts quickly, and there is no size limit on scripts, so a parse check would not have protected the server. |
| A lone `\r` ends a line for starlark-go but is a space to buildifier, so `return \r0` becomes `return 0`. `++x` comes out as `+(+x)`. | Rare. |

Making buildifier safe would have meant `FormatWithoutRewriting`, a size cap, and a reparse-and-compare step that falls back to the raw script. That fallback would trigger on many real scripts because of the octal problem, and the output would still not wrap long lines.

The printer avoids these by design:

- It parses with the same `sourceOptions()` that `Run` uses (`format.go:34`, `codemode.go:131`), so any script Code Mode can run, the printer can parse.
- It writes every literal from `Literal.Raw` (`format.go:325-326`), so strings, bytes and numbers come out exactly as the model wrote them, and requoting cannot happen.
- It breaks calls, lists, dicts, parameters and comprehensions one item per line when they pass 80 columns (`formatWidth`, `format.go:11`). A call with a single argument stays on its line, and only the list inside it breaks.
- It keeps every comment. A comment that cannot stay where it was moves to the end of its line.

Formatting changes only what Web shows. `internal/rocketclaw/frontend/rpc/server.go:911-921` rewrites only the `code` field of an `execute` call's arguments in the history projection and keeps every other field. When the script does not parse, Web shows it as written. The model's own history is untouched.

## Why This Matters

Buildifier is the obvious choice for formatting Starlark, and our printer is about 390 lines of code, so someone will propose the swap. The tests catch most of the harm. The exact-output case "literals as written" in `TestFormat` holds non-ASCII text, `while`, tab indentation and a bytes literal, so buildifier would fail it. The tests do not show the cost of `build.Format`'s argument reordering, which no case covers, or the quadratic slowdown. They also do not say that the problems were measured rather than guessed. Without this record, the next person would run the stress test again.

## When to Apply

- Before adding a Starlark formatting or linting dependency anywhere scripts are run or shown.
- When changing `sourceOptions()`. The printer and `Run` must keep sharing it, or the printer will reject scripts that run, or accept scripts that don't.
- When extending `format.go` for new syntax. `FuzzFormat` (`internal/rocketclaw/codemode/format_test.go:138`) checks every input the same four ways the buildifier harness did: the output parses, means the same program (`syntaxTree` compares the trees), keeps every comment, and is stable. Run it for minutes with `go test -run '^$' -fuzz FuzzFormat -fuzztime 10m ./internal/rocketclaw/codemode/`, not just the seeds that `make test` runs.

## Examples

What the printer does with a squeezed tool call:

```python
def main():
    return github_create_pull_request(owner="Rocketable", repo="platform", title="show the script first", draft=False)
```

becomes

```python
def main():
    return github_create_pull_request(
        owner="Rocketable",
        repo="platform",
        title="show the script first",
        draft=False,
    )
```

In the stress test, buildifier (`FormatWithoutRewriting`) left a longer version of this call, with seven arguments and over 200 characters, on one line. It changed only the spacing around each `=`.

Fuzzing found two bugs in our own printer while it was being written. Tests now guard both:

- `0 .real` printed as `0.real`, which parses as a float followed by a name. `format.go:350-355` keeps the space after an integer. The fuzz seed `testdata/fuzz/FuzzFormat/03344c5ef118c4b4` (`0 .A`) and the "literals as written" case cover it.
- `(1,)` lost its comma when the tuple code changed, which turned a one-element tuple into a number. The "remaining syntax" case covers it.

Two starlark-go details matter when changing the fuzz check. starlark-go drops a comment written straight after a number with no space (`0#c`), so the check compares against the comments in the original parse tree, not against the source text. And the parse tree records the file name, so both parses use `codemode.star`.

## Related

- `docs/solutions/runtime-errors/execute-uncapped-results-exceeded-model-context.md`, another Execute learning, on how big Execute results are capped.
