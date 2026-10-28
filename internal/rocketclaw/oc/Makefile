cloc:
	@printf "JS/TS cloc (internal/rocketclaw/oc/)\n"
	@set -eu; \
	count() { cloc --json --include-lang=JavaScript,TypeScript --fullpath --not-match-d='(node_modules|dist)' "$$@" | jq -r '.SUM.code // 0'; }; \
	tests='(\.test\.[cm]?[jt]sx?)$$'; \
	printf "%-26s %s\n" "copied source cloc:" "$$(count --not-match-f="$$tests" packages)"; \
	printf "%-26s %s\n" "copied test cloc:" "$$(count --match-f="$$tests" packages)"; \
	printf "%-26s %s\n" "host source cloc:" "$$(count --not-match-f="$$tests" host)"; \
	printf "%-26s %s\n" "host test cloc:" "$$(count --match-f="$$tests" host)"

lint:
	bunx tsc --noEmit

# OPENCODE_CHANNEL is read by packages/app/vite.js; Bun runs Vite so the output does not vary with the
# installed Node version.
build:
	OPENCODE_CHANNEL=prod bun --bun vite build

# Each package's flags and preloads follow its upstream package.json test scripts and bunfig.toml.
test:
	cd packages/app && bun test --conditions=solid --only-failures --isolate --preload ../ui/test/solid-jsx.ts --preload ./happydom.ts ./src
	cd packages/app && bun test --conditions=browser --isolate --preload ../ui/test/solid-jsx.ts --preload ./happydom.ts ./test-browser
	cd packages/ui && bun test --only-failures --preload ./test/solid-jsx.ts src
	cd packages/session-ui && bun test --only-failures --preload ../ui/test/solid-jsx.ts src
	cd packages/gui-plugins && bun test --preload ./happydom.ts src
	cd packages/client && bun test --timeout 5000
	cd packages/schema && bun test --only-failures
	cd packages/util && bun test --only-failures
	bun test --conditions=browser --isolate --preload ./host/test/solid-dom.ts --preload ./packages/app/happydom.ts host

.PHONY: cloc lint build test
