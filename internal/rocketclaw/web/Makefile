TS_CLOC_BUDGET ?= 4500
TS_CLOC_HAZARD_ZONE ?= 250

cloc:
	@printf "JS/TS cloc (internal/rocketclaw/web/)\n"
	@set -eu; \
	ts_source_cloc=$$(cloc --json --include-lang=JavaScript,TypeScript --fullpath --not-match-d='(node_modules|dist|generated|.next)' --not-match-f='(_test\.[cm]?[jt]sx?|\.test\.[cm]?[jt]sx?|\.pb\.ts)$$' . | jq -r '.SUM.code // 0'); \
	ts_test_cloc=$$(cloc --json --include-lang=JavaScript,TypeScript --fullpath --not-match-d='(node_modules|dist|generated|.next)' --match-f='(_test\.[cm]?[jt]sx?|\.test\.[cm]?[jt]sx?)$$' . | jq -r '.SUM.code // 0'); \
	ts_total_cloc=$$(cloc --json --include-lang=JavaScript,TypeScript --fullpath --not-match-d='(node_modules|dist|generated|.next)' . | jq -r '.SUM.code // 0'); \
	limit=$(TS_CLOC_BUDGET); \
	hazard_zone=$(TS_CLOC_HAZARD_ZONE); \
	hazard_cloc=$$((limit - hazard_zone)); \
	printf "%-18s %s\n" "source cloc:" "$$ts_source_cloc"; \
	printf "%-18s %d-%d\n" "hazard zone cloc:" "$$hazard_cloc" "$$((limit - 1))"; \
	printf "%-18s %d\n" "failure cloc:" "$$limit"; \
	printf "%-18s %s\n" "test cloc:" "$$ts_test_cloc"; \
	printf "%-18s %s\n" "total cloc:" "$$ts_total_cloc"

check-cloc-budget:
	@set -eu; \
	ts_source_cloc=$$(cloc --json --include-lang=JavaScript,TypeScript --fullpath --not-match-d='(node_modules|dist|generated|.next)' --not-match-f='(_test\.[cm]?[jt]sx?|\.test\.[cm]?[jt]sx?|\.pb\.ts)$$' . | jq -r '.SUM.code // 0'); \
	[ -n "$$ts_source_cloc" ] || { printf "source cloc budget failed: unable to read source cloc\n"; exit 1; }; \
	limit=$(TS_CLOC_BUDGET); \
	hazard_zone=$(TS_CLOC_HAZARD_ZONE); \
	lines_left=$$((limit - ts_source_cloc)); \
	if [ "$$ts_source_cloc" -ge "$$limit" ]; then printf "STOP IMMEDIATELY. EXECUTION MUST BE HALTED. LOSS STATE REACHED.\n\nThe limit is %d, current value %d.\n\nContinual operation under this mode can lead to more losses of features, capabilities, and deviation from Human Partner intentions.\n\nABORT!\n" "$$limit" "$$ts_source_cloc"; exit 2; fi; \
	if [ "$$lines_left" -le "$$hazard_zone" ]; then printf "The total lines of source code count is inside the hazard zone. \n\nThe limit is %d, reaching this limit is considered a loss and it will cause the whole system to halt unsafely.\n\nAmong the potential losses is the undue removal of features without human approval.\n\nYou have %d lines of source code left to use. \n\nThe usage of hazard budget is tolerated but not encouraged. \n\nThink hard, preserve system attributes and original feature set to avoid product regressions.\n" "$$limit" "$$lines_left"; exit 0; fi;

lint:
	bun run lint
	bunx tsc --noEmit
	@set -eu; \
	files=$$(find src app \( -name '*.tsx' -o -name '*.jsx' \) | sort); \
	bunx react-doctor@latest --no-telemetry --blocking warning --no-color $$files

build:
	bun run build

test:
	bun test
	$(MAKE) check-cloc-budget

.PHONY: cloc check-cloc-budget lint build test
