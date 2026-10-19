#!/bin/sh
# State Store equivalence run: reads one database copy through the base tree's code and through
# this tree's code, and compares the two captures. See README.md.
#
# Usage: run.sh <restored-database> [postgres-base-url] [base-revision]
#   <restored-database>  database on the local Postgres holding the restored dump (left untouched)
#   postgres-base-url    default postgres://postgres:postgres@127.0.0.1:5432
#   base-revision        jj revision of the before tree, default heads(::main@origin & ::@) (the PR base)
# Environment:
#   SEARCH_PATH  the State Store's schema when it is not the role's default (e.g. rocketclaw)
#   OUT          output directory, default <repo>/.tmp/equivalence/run-<restored-database>
#   KEEP=1       keep the scratch databases (<db>_equiv_drained, _before, _after) afterwards
#   FIXTURE=1    first create <restored-database>, which must not exist, holding the small fixture
#                the base tree's code builds
#
# Steps, each timed in $OUT/run.log: create or reuse a jj workspace of the base revision under
# <repo>/.tmp/equivalence/ and copy the before files in; clone the restored database; drain the
# clone with the base tree; clone the drained copy twice; capture BEFORE on one; capture AFTER on
# the other (its startup migrates it); compare the captures into report.md.
set -eu
umask 077

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../../../../.." && pwd)

SRC=${1:?usage: run.sh <restored-database> [postgres-base-url] [base-revision]}
PG=${2:-postgres://postgres:postgres@127.0.0.1:5432}
BASE=${3:-heads(::main@origin & ::@)}
OUT=${OUT:-$ROOT/.tmp/equivalence/run-$SRC}
DRAINED=${SRC}_equiv_drained
COPY_BEFORE=${SRC}_equiv_before
COPY_AFTER=${SRC}_equiv_after

COMMIT=$(jj --ignore-working-copy -R "$ROOT" log --no-graph -r "$BASE" -T 'commit_id ++ "\n"')
case $COMMIT in
*[!0-9a-f]* | "")
	echo "base revision $BASE must resolve to exactly one commit" >&2
	exit 2
	;;
esac
BEFORE_TREE=$ROOT/.tmp/equivalence/base-$(printf %.12s "$COMMIT")

mkdir -p "$OUT/tmp"
export TMPDIR="$OUT/tmp" GOFLAGS=-count=1

query="sslmode=disable"
if [ -n "${SEARCH_PATH:-}" ]; then
	query="$query&options=-csearch_path%3D$SEARCH_PATH"
fi

url() { printf '%s/%s?%s' "$PG" "$1" "$query"; }

log() { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" | tee -a "$OUT/run.log"; }

# step NAME COMMAND...: runs COMMAND, logging its wall time; a failing step stops the run.
step() {
	name=$1
	shift
	log "start $name"
	started=$(date +%s)
	if "$@"; then
		log "done  $name in $(($(date +%s) - started))s"
	else
		status=$?
		log "FAIL  $name after $(($(date +%s) - started))s (exit $status)"
		exit "$status"
	fi
}

# workspace checks out the base revision and gives it the before files: this tree's
# equivalence_test.go above its tree adapter, so the shared code cannot drift, then the base
# tree's adapter, drain and fixture. The base tree must build them.
workspace() {
	if [ ! -d "$BEFORE_TREE" ]; then
		jj -R "$ROOT" workspace add --name "equivalence-base-$(printf %.12s "$COMMIT")" -r "$COMMIT" "$BEFORE_TREE" || return
	fi

	parent=$(jj --ignore-working-copy -R "$BEFORE_TREE" log --no-graph -r @- -T commit_id) || return
	if [ "$parent" != "$COMMIT" ]; then
		echo "$BEFORE_TREE is at $parent, not $COMMIT" >&2
		return 1
	fi

	pkg=$BEFORE_TREE/internal/rocketclaw/backend
	sed '/^\/\/ ---- Tree adapter:/,$d' "$ROOT/internal/rocketclaw/backend/equivalence_test.go" >"$pkg/equivalence_test.go" &&
		sed "s/BASE_COMMIT/$COMMIT/" "$HERE/equivalence_adapter_test.go.txt" >"$pkg/equivalence_adapter_test.go" &&
		cp "$HERE/equivalence_drain_test.go.txt" "$pkg/equivalence_drain_test.go" &&
		cp "$HERE/equivalence_fixture_test.go.txt" "$pkg/equivalence_fixture_test.go" &&
		(cd "$pkg" && go test -run '^$' ./) >"$OUT/workspace.log" 2>&1
}

fixture() {
	psql -q -v ON_ERROR_STOP=1 "$PG/postgres?sslmode=disable" -c "CREATE DATABASE \"$SRC\"" &&
		mkdir -p "$OUT/fixture-workspace" &&
		(cd "$BEFORE_TREE/internal/rocketclaw/backend" &&
			ROCKETCLAW_EQUIV_FIXTURE_URL=$(url "$SRC") ROCKETCLAW_EQUIV_FIXTURE_WORKSPACE="$OUT/fixture-workspace" go test -timeout 10m -run '^TestStateStoreEquivalenceFixture$' -v ./) >"$OUT/fixture.log" 2>&1
}

clone() { psql -q -v ON_ERROR_STOP=1 "$PG/postgres?sslmode=disable" -c "DROP DATABASE IF EXISTS \"$2\"" -c "CREATE DATABASE \"$2\" TEMPLATE \"$1\""; }

drain() {
	(cd "$BEFORE_TREE/internal/rocketclaw/backend" &&
		ROCKETCLAW_EQUIV_DATABASE_URL=$(url "$DRAINED") go test -timeout 0 -run '^TestStateStoreEquivalenceDrain$' -v ./) >"$OUT/drain.log" 2>&1
}

capture() {
	(cd "$1/internal/rocketclaw/backend" &&
		ROCKETCLAW_EQUIV_DATABASE_URL=$(url "$2") ROCKETCLAW_EQUIV_OUT="$OUT/$3.jsonl" go test -timeout 0 -run '^TestStateStoreEquivalenceCapture$' -v ./) >"$OUT/$3.log" 2>&1
}

report() {
	# The report test fails on unexplained breaks but still writes the report.
	(cd "$ROOT/internal/rocketclaw/backend" &&
		ROCKETCLAW_EQUIV_BEFORE="$OUT/before.jsonl" ROCKETCLAW_EQUIV_AFTER="$OUT/after.jsonl" ROCKETCLAW_EQUIV_REPORT="$OUT/report.md" \
			go test -timeout 0 -run '^TestStateStoreEquivalenceReport$' -v ./) >"$OUT/report.log" 2>&1 || log "report: unexplained differences (see report.md)"
	test -s "$OUT/report.md"
}

log "run on $SRC at $PG${SEARCH_PATH:+ (search_path $SEARCH_PATH)}, base $COMMIT ($BASE); output in $OUT"
step base-workspace workspace
if [ "${FIXTURE:-}" = 1 ]; then
	step build-fixture fixture
fi
step clone-for-drain clone "$SRC" "$DRAINED"
step drain-before-tree drain
step clone-before clone "$DRAINED" "$COPY_BEFORE"
step clone-after clone "$DRAINED" "$COPY_AFTER"
step capture-before capture "$BEFORE_TREE" "$COPY_BEFORE" before
step capture-after-migrates capture "$ROOT" "$COPY_AFTER" after
step report report

if [ "${KEEP:-}" != 1 ]; then
	psql -q "$PG/postgres?sslmode=disable" -c "DROP DATABASE IF EXISTS \"$DRAINED\"" -c "DROP DATABASE IF EXISTS \"$COPY_BEFORE\"" -c "DROP DATABASE IF EXISTS \"$COPY_AFTER\""
fi

sed -n '/^## Summary/,/^## Timings/p' "$OUT/report.md" | sed '$d'
log "report: $OUT/report.md"
