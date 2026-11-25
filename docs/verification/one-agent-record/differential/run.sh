#!/bin/bash
set -euo pipefail
: "${OLD_TREE:?}" "${NEW_TREE:?}" "${ROCKETCLAW_TEST_DATABASE_URL:?}"
here=$(cd "$(dirname "$0")" && pwd)
WORK=${WORK:-$here/../../../../.tmp/one-agent-record-diff}
mkdir -p "$WORK/tmp"
WORK=$(cd "$WORK" && pwd)
old_test=$OLD_TREE/internal/rocketclaw/backend/zz_diff_test.go
new_test=$NEW_TREE/internal/rocketclaw/backend/zz_diff_test.go
trap 'rm -f "$old_test" "$new_test"' EXIT
sed 's/__WRITE_ARGS__/, agents/' "$here/zz_diff_test.go.tmpl" > "$old_test"
sed 's/__WRITE_ARGS__//' "$here/zz_diff_test.go.tmpl" > "$new_test"
python3 "$here/world.py" "$WORK"
for version in old new; do
	tree=$OLD_TREE
	[ "$version" = new ] && tree=$NEW_TREE
	TMPDIR=$WORK/tmp GOTMPDIR=$WORK/tmp ZZ_DIFF_WORLD=$WORK/world-$version.json ZZ_DIFF_OUT=$WORK/out-$version.jsonl \
		go -C "$tree/internal/rocketclaw" test -count=1 -timeout 60m -run '^TestZZDelegationDiff$' ./backend
done
python3 "$here/compare.py" "$WORK" "$here/../README.md"
