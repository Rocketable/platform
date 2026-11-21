# Comparison rewrite in `execute`: Lean proof

Inside `execute`, tool calls return typed values such as `text`. Starlark never treats values of different types as
equal, so without help `read(...) == "abc"` would always be `False`. The fix rewrites the parsed script before it is
compiled: in every `==`, `!=`, `<`, `<=`, `>` and `>=` both operands are unboxed first, and in every `in` and `not in`
the left operand is unboxed and the container's elements are compared after unboxing. `CompareRewrite.lean` models
Starlark's comparisons and this rewrite, and proves that the rewrite keeps plain scripts unchanged, makes boxed text
compare like its text, and only touches comparison nodes. **A pass holds for this model, not for the Go code.** The Go
code must follow the spec below for the proof to say anything about it.

## Run

From the repository root:

```sh
make -C docs/verification/typed-execute-compare        # same as `make ... lean`
```

Requires `curl`, `shasum`, `tar` with zstd, and Make. The Makefile downloads Lean 4.36.0 for this machine into the
repository root's `.tmp/lean/` and checks its SHA-256, with the same pins as
`docs/verification/tab-selection-escape/Makefile`. It writes Lean's output to `.tmp/run/typed-execute-compare/lean.log`.
The target fails when Lean exits non-zero, when the output contains `error`, `warning` or `sorry`, when the file uses
`native_decide`, when a `#print axioms` line is missing, or when any printed theorem depends on an axiom other than
`propext`, `Classical.choice` and `Quot.sound`.

## Model

| Model | Go |
|---|---|
| `Value.str`, `int`, `bool`, `noneValue`, `list`, `dict` (a list of key/value pairs) | Starlark `string`, `int`, `bool`, `None`, `list`/`tuple`, `dict` |
| `Value.box v` | a typed value that `$unbox` opens: a text-like value (`text`, `bash_result`, `webfetch_result`, `mcp_result`, `path`) is `box (str t)`, a `dict_result` is `box (dict …)` |
| `Value.obj kind id` | a typed value that `$unbox` leaves alone (`file`, `match`, `glob_result`, `grep_result`): equal only to the same object of the same kind; ordering raises |
| `Value.fail m` | an `error` result with message `m` |
| `unbox policy` | `$unbox`. `policy` is how an `error` is unboxed: `message` gives its message as a string, which is what the Go code does; `opaque` leaves it unchanged. Every theorem about boxes holds for both. |
| `starEq` | `starlark.Equal`: same kind compares contents (lists element by element, dicts by length and key lookup), two boxes compare their contents, two errors compare messages, different kinds are never equal |
| `ordered` | `starlark.Compare` for `<`, `<=`, `>`, `>=`: strings, ints and bools are ordered, lists lexicographically (first unequal element decides, then length), two boxes by their contents; anything else, including mixed kinds, `None`, dicts and errors, raises (`none`) |
| `memberValues` | `starlark.Binary(syntax.IN, …)`: a list matches when some element `e` has `Equal(e, x)`, a dict when some key `k` has `Equal(x, k)`, a string when the needle is a string and a substring; anything else raises; a box container passes the test to its contents |
| `compareRewritten`, `memberRewritten` | what a rewritten comparison computes |
| `Expr`, `eval`, `World` | a small Starlark: literals, variables, host calls that append to an effect log, comparisons, `in`/`not in`, `not`, `and`, `or`, `if`. `$unbox(e)` and `$unbox_elems(e)` are their own nodes; scripts cannot write them (`scriptSource`). A box is truthy exactly when its contents are; an error is falsy. |
| `rewrite` | the parse-time rewrite |

Floats are left out (Lean's `Float` has no usable equality), so Starlark's int/float mixing is not modeled. `$unbox`
leaves numbers unchanged and never produces one, so that mixing only ever happens between plain values, which the
rewrite does not change. Tuples behave like lists. Sets, ranges and bytes are not modeled.

## Spec for the Go rewrite

This is what the Go code must do for the model to match it.

1. **Where.** Rewrite every `BinaryExpr` whose operator is `==`, `!=`, `<`, `<=`, `>`, `>=`, `in` or `not in`, in
   every function, lambda and comprehension. Rewrite nothing else. The operator stays the same; each operand is still
   evaluated once, left to right; the new calls add no effects.
2. **`$unbox(v)`.** A text-like value (`codemode.Text`) gives `starlark.String(v.Text())`; a `dict_result` gives its
   dict; every other value is returned unchanged. It looks at the top level only: a list that holds texts is returned
   as it is. An `error` gives its message. If errors were instead left opaque, the only change would be that `$unbox`
   leaves an `error` unchanged (the `opaque` policy).
3. **`==`, `!=`, `<`, `<=`, `>`, `>=`.** `a OP b` becomes `$unbox(a) OP $unbox(b)`.
4. **`in`, `not in`.** `x in y` is true exactly when, with `u = $unbox(x)` and `c = $unbox(y)`:
   - `c` is a list or tuple and some element `e` has `starlark.Equal(u, $unbox(e))`;
   - `c` is a dict and some key `k` has `starlark.Equal(u, $unbox(k))`. **This includes text keys tested with a plain
     string**: `"a" in {text("a"): 1}` is `True`;
   - `c` is a string: `u` must be a string (otherwise the usual `'in <string>' requires string as left operand` error)
     and is a substring of `c`;
   - anything else: whatever `starlark.Binary(syntax.IN, u, c)` gives, error included.

   `x not in y` is the negation of `x in y`, and raises exactly when `x in y` raises. Any implementation that gives
   these results is fine, for example `$unbox(x) in $unbox_elems(y)` where `$unbox_elems` unboxes `y` and returns a
   new list, tuple or dict with unboxed elements or keys (the model's form), or a dict lookup of `u` first with a scan
   of the keys only when that misses and the dict holds a boxed key. Sets are not modeled.
5. **Plain values.** When neither operand is a box, and for `in` neither the needle nor any element or key of the
   container is a box, the rewritten comparison gives exactly the old result, errors included (theorems
   `compare_conservative*`, `member_conservative*`, `rewrite_conservative`).

There is deliberately no second test with the original, still boxed `x`: for a string container it would raise
(`'in <string>' requires string as left operand, not text`) where the answer is `False`, and for a dict it is
already covered by comparing unboxed keys.

## Theorems

Every theorem holds for every value, and for both ways of unboxing an error unless its name says `opaque` or `message`.

| Theorem | What it says |
|---|---|
| `unbox_idem` | When a box never holds a box or an error, unboxing twice is unboxing once, and the result is never a box. |
| `compare_conservative` | If unboxing leaves both operands unchanged, `a OP b` gives the old result, for all six operators. |
| `compare_conservative_without_boxes` | The same, stated as "neither operand is a box". |
| `member_conservative` | If unboxing leaves the needle unchanged, leaves the container unchanged or the container is a box, and leaves each element or key of the unboxed container unchanged, `in` and `not in` give the old result. |
| `member_conservative_without_boxes` | The same, stated as "the needle, the container and its elements or keys are not boxes". |
| `compare_box_left`, `compare_box_right` | `box v OP b` gives what `v OP b` gave before, and `a OP box v` what `a OP v` gave, for every operator and plain other operand. |
| `compare_box_box` | Two boxes compare as their contents, which is also what they did before the rewrite. |
| `ne_is_not_eq` | After the rewrite, `a != b` is the negation of `a == b`. |
| `box_text_eq` | `box s == s'` and `s' == box s` are `s == s'`. |
| `box_text_compares_as_text` | Boxed text compares with text, in either order or boxed on both sides, exactly as the texts do, for all six operators. |
| `text_order_is_string_order` | `<`, `<=`, `>`, `>=` on text follow Lean's string order (which is Starlark's byte order). |
| `old_eq_box_text_false`, `new_eq_box_text_true` | `box "a" == "a"` was `False` and is now `True`. |
| `old_lt_box_text_raises`, `new_lt_box_text_true` | `box "a" < "b"` used to raise and is now `True`. |
| `member_elements_list`, `member_keys_dict` | `in` on a list or dict is "some unboxed element or key equals the unboxed needle", negated for `not in`. |
| `not_in_is_not_in` | `not in` is the negation of `in`, and raises exactly when `in` raises. |
| `member_box_list`, `member_box_dict`, `member_box_text` | `box v in c` gives what `v in c` gave for a list or dict of plain values, and boxed text in text (either side boxed) is a substring test. |
| `member_boxed_elements` | A plain needle in a list of boxes gives what it gave in the list of their contents. |
| `old_box_not_in_list`, `new_box_in_list` | `box "a" in ["a"]` was `False` and is now `True`. |
| `old_text_not_in_list_of_boxes`, `new_text_in_list_of_boxes` | `"a" in [box "a"]` was `False` and is now `True`. |
| `old_box_in_text_raises`, `new_box_in_text` | `box "b" in "abc"` used to raise and is now `True`. |
| `opaque_failure_never_equals_plain` | With opaque errors, an error is never `==` (always `!=`) any value that does not unbox to an error, in either order. |
| `opaque_failure_ordering_raises` | With opaque errors, `<`, `<=`, `>`, `>=` with an error raise. |
| `opaque_failure_never_member_of_plain_list`, `…_dict` | With opaque errors, an error is never `in` (always `not in`) a list or dict whose elements or keys do not unbox to errors. |
| `opaque_failure_in_text_raises` | With opaque errors, `error in "text"` raises. |
| `message_failure_compares_as_message`, `message_failure_member_as_message` | With `message` unboxing, an error behaves in every comparison and membership test exactly like its message string. |
| `message_failure_equals_only_its_message` | With `message` unboxing, an error is `==` exactly the values that unbox to its message. |
| `rewrite_unboxes_exactly_at_comparisons` | In the rewrite of any script, `$unbox` appears around both operands of every comparison and around the needle of every `in`/`not in`, `$unbox_elems` around every container, and nowhere else. |
| `rewrite_only_adds_unbox` | Removing the `$unbox` and `$unbox_elems` nodes from the rewrite of any script gives back that script: nothing else changes. |
| `rewrite_is_the_only_such_tree` | A tree with that shape is the rewrite of what remains after removing those nodes, so the two theorems above pin the rewrite down completely. |
| `eval_unbox_adds_no_effect` | `$unbox` and `$unbox_elems` add no effects. |
| `eval_rewritten_comparison`, `eval_rewritten_member` | A rewritten comparison or membership test evaluates the left operand, then the right one, each once, and its value is `compareRewritten` or `memberRewritten` of the two results. |
| `rewrite_conservative` | When the environment, every host-call result and every literal is free of boxes at the top level and in its elements (and, with `message` unboxing, of errors), the rewritten script gives the same value **and the same effect log** as the original, for every script. |
| `rewrite_can_change_later_effects` | A witness that this hypothesis is needed: `if x == "a": notify()` with `x = box "a"` calls `notify` only after the rewrite. |

The rewrite does not keep the effect log of every script: once an `if` depends on a comparison whose answer the
rewrite changes, a different branch runs, as `rewrite_can_change_later_effects` shows. What holds is the local
property (`eval_unbox_adds_no_effect`, `eval_rewritten_comparison`, `eval_rewritten_member`: same operands, same order,
no new effects) plus `rewrite_conservative` for scripts without boxes.

## Recorded run

2026-11-21, `make -C docs/verification/typed-execute-compare`, Lean 4.36.0 (linux aarch64, commit `611c117b`). The
first run downloaded and unpacked Lean, passed the SHA-256 check, and exited 0 in 12.6 s; reruns exited 0 in 2.9 s to 5.5 s,
of which Lean alone takes about 2.6 s. Lean printed nothing besides the 45 `#print axioms` lines. Every theorem depends only on
standard axioms: `rewrite_unboxes_exactly_at_comparisons` and `rewrite_only_adds_unbox` on `propext`;
`unbox_idem` and `rewrite_is_the_only_such_tree` on `propext` and `Quot.sound`; every other printed theorem on
`propext`, `Classical.choice` and `Quot.sound` (the well-founded definition of `starEq` brings in `Classical.choice`).
No theorem uses `sorry` or `native_decide`.

Two checks that the gate can fail: a deliberately false theorem, that the rewrite keeps `box "a" == "a"` equal to its
old answer, was rejected with the unproved goal `False`; and a planted `theorem planted : True := sorry` made the target
fail with `declaration uses 'sorry'` and `'planted' depends on axioms: [sorryAx]` (make exit 2). Both were removed.

## Assumptions and what is left out

- The proof covers the rewrite rule over this model, not starlark-go or the Go rewrite. Tests in `codemode` must check
  that the Go code matches the spec above, including that every comparison in a parsed script is rewritten.
- Boxes are never nested, and a box holds a plain value: `$unbox` never returns a typed value.
- Two errors compare by message. With `message` unboxing both are strings by the time they are compared, so the
  `error` type's own comparison does not matter.
- Dict membership in starlark-go hashes the needle first and treats an unhashable needle as "not found"; the model
  compares keys directly, which gives the same answer for dicts whose keys are hashable.
- Recursion depth limits and Starlark's step budget are not modeled. Error messages are modeled only as "raises".
