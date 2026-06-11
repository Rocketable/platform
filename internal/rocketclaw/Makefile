COVERPROFILE ?= .coverage.out
COVERPKG ?= github.com/Rocketable/platform/cmd/rocketclaw,./...
COVERAGE_STABLE_AT ?= 90.0
GO_SOURCE_CLOC_BUDGET ?= 17000
GO_SOURCE_CLOC_HAZARD_ZONE ?= 250

run:
	go run cirello.io/runner/v3@latest

vendor: .FORCE
	go mod tidy
	go mod vendor

generate: vendor
	go generate ./discordvoice/internal/aesgcm8

lint: vendor
	go run -mod=readonly github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.4 run --fix ./...

build: generate lint
	go build -o ../../bin/rocketclaw ../../cmd/rocketclaw

cloc:
	@printf "Go cloc (cmd/rocketclaw and internal/rocketclaw, excluding generated discordvoice/internal/aesgcm8 only)\n"
	@set -eu; \
	go_source_cloc=$$(cloc --json --include-lang=Go --fullpath --not-match-d='discordvoice/internal/aesgcm8' --not-match-f='(_test\.go)$$' ../../cmd/rocketclaw . | jq -r '.SUM.code'); \
	go_test_cloc=$$(cloc --json --include-lang=Go --fullpath --not-match-d='discordvoice/internal/aesgcm8' --match-f='(_test\.go)$$' ../../cmd/rocketclaw . | jq -r '.SUM.code'); \
	go_total_cloc=$$(cloc --json --include-lang=Go --fullpath --not-match-d='discordvoice/internal/aesgcm8' ../../cmd/rocketclaw . | jq -r '.SUM.code'); \
	limit=$(GO_SOURCE_CLOC_BUDGET); \
	hazard_zone=$(GO_SOURCE_CLOC_HAZARD_ZONE); \
	hazard_cloc=$$((limit - hazard_zone)); \
	printf "%-18s %s\n" "source cloc:" "$$go_source_cloc"; \
	printf "%-18s %d-%d\n" "hazard zone cloc:" "$$hazard_cloc" "$$((limit - 1))"; \
	printf "%-18s %d\n" "failure cloc:" "$$limit"; \
	printf "%-18s %s\n" "test cloc:" "$$go_test_cloc"; \
	printf "%-18s %s\n" "total cloc:" "$$go_total_cloc"

test: generate lint
	go test -race -count 1 -covermode=atomic -coverpkg=$(COVERPKG) -coverprofile=$(COVERPROFILE) github.com/Rocketable/platform/cmd/rocketclaw/... ./...
	$(MAKE) check-code-metrics

tests: test

check-code-metrics:
	$(MAKE) check-coverage
	$(MAKE) check-cloc-budget

check-cloc-budget:
	@set -eu; \
	go_source_cloc=$$(cloc --json --include-lang=Go --fullpath --not-match-d='discordvoice/internal/aesgcm8' --not-match-f='(_test\.go)$$' ../../cmd/rocketclaw . | jq -r '.SUM.code'); \
	[ -n "$$go_source_cloc" ] || { printf "source cloc budget failed: unable to read source cloc\n"; exit 1; }; \
	limit=$(GO_SOURCE_CLOC_BUDGET); \
	hazard_zone=$(GO_SOURCE_CLOC_HAZARD_ZONE); \
	lines_left=$$((limit - go_source_cloc)); \
	if [ "$$go_source_cloc" -ge "$$limit" ]; then printf "EXECUTION MUST BE HALTED. LOSS STATE REACHED.\n\nThe limit is %d, current value %d.\n\nContinual operation under this mode can lead to more losses of features, capabilities, and deviation from Human Partner intentions.\n\nABORT!\n" "$$limit" "$$go_source_cloc"; exit 2; fi; \
	if [ "$$lines_left" -le "$$hazard_zone" ]; then printf "The total lines of source code count is inside the hazard zone. \n\nThe limit is %d, reaching this limit is considered a loss and it will cause the whole system to halt unsafely.\n\nAmong the potential losses is the undue removal of features without human approval.\n\nYou have %d lines of source code left to use. \n\nThe usage of hazard budget is tolerated but not encouraged. \n\nThink hard, preserve system attributes and original feature set to avoid product regressions.\n" "$$limit" "$$lines_left"; exit 0; fi; \

check-coverage:
	@test -f "$(COVERPROFILE)" || { printf "missing coverage profile %s (run make test first)\n" "$(COVERPROFILE)"; exit 1; }
	@set -eu; \
	tmp_dir=.tmp; \
	mkdir -p "$$tmp_dir"; \
	base_dir=$$(mktemp -d "$$tmp_dir/rocketclaw-coverage-base-XXXXXX"); \
	rmdir "$$base_dir"; \
	cleanup() { if [ -d "$$base_dir" ]; then jj --ignore-working-copy --repository "$$base_dir" workspace forget >/dev/null 2>&1 || true; fi; rm -rf "$$base_dir"; rmdir "$$tmp_dir" >/dev/null 2>&1 || true; }; \
	trap cleanup EXIT INT TERM; \
	jj --quiet workspace add --revision @- "$$base_dir"; \
	(cd "$$base_dir" && go mod tidy && go mod vendor && go test -race -count 1 -covermode=atomic -coverpkg=$(COVERPKG) -coverprofile=$(COVERPROFILE) github.com/Rocketable/platform/cmd/rocketclaw/... ./...); \
	base=$$(cd "$$base_dir" && go tool cover -func="$(COVERPROFILE)" | awk '/^total:/ { gsub("%", "", $$3); print $$3 }'); \
	current=$$(go tool cover -func="$(COVERPROFILE)" | awk '/^total:/ { gsub("%", "", $$3); print $$3 }'); \
	[ -n "$$base" ] && [ -n "$$current" ] || { printf "coverage budget failed: unable to read coverage totals\n"; exit 1; }; \
	awk -v base="$$base" -v current="$$current" -v stable_at="$(COVERAGE_STABLE_AT)" 'BEGIN { if (current + 0 < stable_at + 0 && current + 0 <= base + 0) { printf "coverage budget failed (coverage must increase until %.1f%%): current %.1f%%, base %.1f%%\n", stable_at, current, base; exit 1 } if (current + 0 < stable_at + 0) { printf "coverage budget ok: current %.1f%% > base %.1f%%\n", current, base; exit 0 } printf "coverage budget ok: current %.1f%% >= stable %.1f%%\n", current, stable_at }'

.FORCE:

deploy:
	@set -eu; \
	pid=$$(pgrep -n -x rocketclaw || true); \
	[ -n "$$pid" ] || { printf "deploy failed: no running rocketclaw process found\n"; exit 1; }; \
	target=$$(lsof -a -p "$$pid" -d txt -Fn | awk '/^n/ { print substr($$0, 2); exit }'); \
	[ -n "$$target" ] || { printf "deploy failed: unable to find running rocketclaw path for pid %s\n" "$$pid"; exit 1; }; \
	printf "deploying rocketclaw to %s (pid %s)\n" "$$target" "$$pid"; \
	$(MAKE) build; \
	mv ./bin/rocketclaw "$$target"; \
	kill "$$pid"

go-mod-update:
	go get -u ./...
	$(MAKE) vendor
