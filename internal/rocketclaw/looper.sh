#!/bin/sh

set -u

while true; do
	make_test_output="$(mktemp)"
	make_test_status="$(mktemp)"
	{ make test 2>&1; printf '%s\n' "$?" >"$make_test_status"; } | tee "$make_test_output"
	make_test_result="$(cat "$make_test_output")"
	make_test_exit="$(cat "$make_test_status")"
	rm -f "$make_test_output" "$make_test_status"

	[ "$make_test_exit" -ne 0 ] || break

	opencode run --model openai/gpt-5.5 --variant xhigh --thinking "$(printf 'make test is failing.

Failure output:
%s

Make changes until it passes.
You are chasing the highest code coverage of the current functionalities at lowest source (non-testing) lines of code count

That means:
- increase code coverage first BEFORE deciding to delete source (non-testing) lines of code.
_CRITICAL_: you must keep the current functionality intact - do not remove features.

Consider using @go-developer @go-reviewer and @project-critic-council

Skip sharing diffs, proceed directly with the necessary changes. Make it so.
' "$make_test_result")"

	echo "opencode done"
done
