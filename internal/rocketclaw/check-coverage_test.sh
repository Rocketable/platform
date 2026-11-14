#!/bin/sh
# Run on Linux: sh internal/rocketclaw/check-coverage_test.sh
set -eu
cd "$(dirname "$0")"
mkdir -p ../../.tmp
scratch=$(mktemp -d "$(pwd)/../../.tmp/check-coverage.XXXXXX")
trap 'rm -rf "$scratch"' EXIT HUP INT TERM

printf 'package fixture\nfunc covered() { println("hit") }\nfunc missed() { println("miss") }\n' > "$scratch/fixture.go"

for test in below near exact above duplicate union generated; do
	case "$test" in
		below) hit=20226; miss=1810; want=fail ;;
		near) hit=917999999; miss=82000001; want=fail ;;
		exact|union|generated) hit=459; miss=41; want=pass ;;
		above) hit=460; miss=40; want=pass ;;
		duplicate) hit=459; miss=42; want=fail ;;
	esac
	{
		printf 'mode: atomic\n%s:2.16,2.32 %s 1\n%s:3.15,3.32 %s 0\n' "$scratch/fixture.go" "$hit" "$scratch/fixture.go" "$miss"
		case "$test" in
			duplicate) printf '%s:2.16,2.32 %s 1\n' "$scratch/fixture.go" "$hit" ;;
			union) printf '%s:2.16,2.32 %s 0\n%s:3.15,3.32 %s 0\n' "$scratch/fixture.go" "$hit" "$scratch/fixture.go" "$miss" ;;
			generated) printf '%s/generated.pb.go:1.1,1.2 10000 0\n' "$scratch" ;;
		esac
	} > "$scratch/coverage.out"
	if make --no-print-directory -o coverage-profile check-coverage COVERPROFILE="$scratch/coverage.out" > "$scratch/output" 2>&1; then
		got=pass
	else
		got=fail
	fi
	cat "$scratch/output"
	[ "$got" = "$want" ] || { printf '%s: got %s, want %s\n' "$test" "$got" "$want"; exit 1; }
	grep -q 'coverage budget' "$scratch/output"
	printf '%s: %s\n' "$test" "$got"
done
