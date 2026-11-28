export ROCKETCLAW_PLAYWRIGHT_MODULE ?= $(HOME)/.cache/playwright-core/node_modules/playwright-core
export ROCKETCLAW_CHROMIUM ?= $(shell bun -e 'console.log(require("$(ROCKETCLAW_PLAYWRIGHT_MODULE)").chromium.executablePath())' 2>/dev/null)
export TMPDIR := $(abspath $(dir $(shell jj git root 2>/dev/null || echo $(CURDIR)/../../../.git)))/.tmp/t
$(shell mkdir -p $(TMPDIR))
TS_CLOC_BUDGET ?= 13550
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
	bun install --frozen-lockfile
	bun run lint
	bunx tsc --noEmit
	@set -eu; \
	files=$$(find src app \( -name '*.tsx' -o -name '*.jsx' \) | sort); \
	bunx react-doctor@latest --no-telemetry --blocking warning --no-color $$files

test:
	go -C .. generate ./internal/web
	@set -eu; \
	db=; \
	cleanup() { \
		if [ -n "$$db" ]; then docker rm -f -v "$$db" >/dev/null 2>&1 || true; fi; \
		pkill -P 1 -f 'ms-playwright/[c]hromium' || true; \
	}; \
	cleanup; \
	trap cleanup EXIT INT TERM; \
	bash -o pipefail -c 'find ./src -type f \( -name "*.test.ts" -o -name "*.test.tsx" \) ! -name "*-transport.test.ts" -exec ls -S {} + | xargs -n 1 -P 4 bun test'; \
	if [ -z "$${ROCKETCLAW_TEST_DATABASE_URL:-}" ]; then \
		db=$$(docker run -d --pull=always -e POSTGRES_HOST_AUTH_METHOD=trust -e POSTGRES_USER=postgres -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=rocketclaw_test -p 127.0.0.1::5432 postgres:latest); \
		port=$$(docker port "$$db" 5432/tcp); \
		port=$${port##*:}; \
		i=0; \
		while [ "$$i" -lt 30 ] && ! docker exec "$$db" pg_isready -U postgres -d rocketclaw_test >/dev/null 2>&1; do i=$$((i + 1)); sleep 1; done; \
		export ROCKETCLAW_TEST_DATABASE_URL="postgres://postgres:postgres@127.0.0.1:$$port/rocketclaw_test?sslmode=disable"; \
	fi; \
	go -C .. test ./frontend/rpc -run 'Test(PublicProgressRealBrowser|RevertRealBackendBrowser|ShadowsRealBackendBrowser|SessionEntries|SidebarHTTPStreamsBeforeGoTailAndCancels|PagesRealBrowser|ReloadRealBackendBrowser|RemoteRocketClawRealBrowser|AgentEditor(EditingAndSaving|PreviewAndChat)RealBackendBrowser)$$' -count=1
	$(MAKE) check-cloc-budget

test-files:
	@test -n "$(TESTS)" || { echo 'name the files: make test-files TESTS="src/tabs.browser.test.ts"' >&2; exit 1; }
	@! printf '%s\n' $(TESTS) | grep -e '-transport\.test\.ts$$' >&2 || { echo 'a Go test in ../frontend/rpc runs each *-transport.test.ts file above (grep runWebTest there): make -C .. coverage-profile GOTESTPKGS=./frontend/rpc GOTESTRUN=<that test>' >&2; exit 1; }
	go -C .. generate ./internal/web
	printf '%s\n' $(TESTS) | xargs -n 1 -P 4 bun test

.PHONY: cloc check-cloc-budget lint test test-files
