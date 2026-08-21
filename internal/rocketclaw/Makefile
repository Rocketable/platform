COVERPROFILE ?= .coverage.out
COVERPKG ?= github.com/Rocketable/platform/cmd/rocketclaw,./...
COVERAGE_STABLE_AT ?= 90.0
GO_SOURCE_CLOC_BUDGET ?= 19250
GO_SOURCE_CLOC_HAZARD_ZONE ?= 500

run:
	go run cirello.io/runner/v3@latest

vendor: .FORCE
	go mod tidy
	go mod vendor

generate: vendor
	go generate ./emoji
	go generate ./skel

lint: vendor
	go run -mod=readonly github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.0 run --fix ./...

build: generate lint
	go build -o ../../bin/rocketclaw ../../cmd/rocketclaw

cloc:
	@printf "Go cloc (cmd/rocketclaw and internal/rocketclaw, excluding generated emoji only)\n"
	@set -eu; \
	go_source_cloc=$$(cloc --json --include-lang=Go --fullpath --not-match-d='emoji' --not-match-f='(_test\.go)$$' ../../cmd/rocketclaw . | jq -r '.SUM.code'); \
	go_test_cloc=$$(cloc --json --include-lang=Go --fullpath --not-match-d='emoji' --match-f='(_test\.go)$$' ../../cmd/rocketclaw . | jq -r '.SUM.code'); \
	go_total_cloc=$$(cloc --json --include-lang=Go --fullpath --not-match-d='emoji' ../../cmd/rocketclaw . | jq -r '.SUM.code'); \
	limit=$(GO_SOURCE_CLOC_BUDGET); \
	hazard_zone=$(GO_SOURCE_CLOC_HAZARD_ZONE); \
	hazard_cloc=$$((limit - hazard_zone)); \
	printf "%-18s %s\n" "source cloc:" "$$go_source_cloc"; \
	printf "%-18s %d-%d\n" "hazard zone cloc:" "$$hazard_cloc" "$$((limit - 1))"; \
	printf "%-18s %d\n" "failure cloc:" "$$limit"; \
	printf "%-18s %s\n" "test cloc:" "$$go_test_cloc"; \
	printf "%-18s %s\n" "total cloc:" "$$go_total_cloc"

test: generate lint
	$(MAKE) check-code-metrics

tests: test

check-code-metrics:
	$(MAKE) check-coverage
	$(MAKE) check-cloc-budget

check-cloc-budget:
	@set -eu; \
	go_source_cloc=$$(cloc --json --include-lang=Go --fullpath --not-match-d='emoji' --not-match-f='(_test\.go)$$' ../../cmd/rocketclaw . | jq -r '.SUM.code'); \
	[ -n "$$go_source_cloc" ] || { printf "source cloc budget failed: unable to read source cloc\n"; exit 1; }; \
	limit=$(GO_SOURCE_CLOC_BUDGET); \
	hazard_zone=$(GO_SOURCE_CLOC_HAZARD_ZONE); \
	lines_left=$$((limit - go_source_cloc)); \
	if [ "$$go_source_cloc" -ge "$$limit" ]; then printf "STOP IMMEDIATELY. EXECUTION MUST BE HALTED. LOSS STATE REACHED.\n\nThe limit is %d, current value %d.\n\nContinual operation under this mode can lead to more losses of features, capabilities, and deviation from Human Partner intentions.\n\nABORT!\n" "$$limit" "$$go_source_cloc"; exit 2; fi; \
	if [ "$$lines_left" -le "$$hazard_zone" ]; then printf "The total lines of source code count is inside the hazard zone. \n\nThe limit is %d, reaching this limit is considered a loss and it will cause the whole system to halt unsafely.\n\nAmong the potential losses is the undue removal of features without human approval.\n\nYou have %d lines of source code left to use. \n\nThe usage of hazard budget is tolerated but not encouraged. \n\nThink hard, preserve system attributes and original feature set to avoid product regressions.\n" "$$limit" "$$lines_left"; exit 0; fi; \

check-coverage:
	@set -eu; \
	name=; \
	base_dir=; \
	cleanup() { \
		if [ -n "$$name" ]; then docker rm -f "$$name" >/dev/null 2>&1 || true; fi; \
		if [ -n "$$base_dir" ] && [ -d "$$base_dir" ]; then jj --ignore-working-copy --repository "$$base_dir" workspace forget >/dev/null 2>&1 || true; rm -rf "$$base_dir"; fi; \
	}; \
	trap cleanup EXIT INT TERM; \
	if [ -z "$${ROCKETCLAW_TEST_DATABASE_URL:-}" ]; then \
		latest=$$(curl -fsSL https://www.postgresql.org/versions.json | jq -r '[.[] | select(.supported) | .major] | max_by(tonumber)'); \
		name=rocketclaw-pg-$$$$; \
		docker run -d --name "$$name" \
			-e POSTGRES_HOST_AUTH_METHOD=trust \
			-e POSTGRES_USER=postgres \
			-e POSTGRES_PASSWORD=postgres \
			-e POSTGRES_DB=rocketclaw_test \
			-p 127.0.0.1:55432:5432 \
			postgres:$$latest >/dev/null; \
		i=0; \
		while [ "$$i" -lt 30 ]; do \
			if docker exec "$$name" pg_isready -U postgres -d rocketclaw_test >/dev/null 2>&1; then break; fi; \
			i=$$((i + 1)); \
			sleep 1; \
		done; \
		export ROCKETCLAW_TEST_DATABASE_URL="postgres://postgres:postgres@127.0.0.1:55432/rocketclaw_test?sslmode=disable"; \
	fi; \
	go test -race -count 1 -covermode=atomic -coverpkg=$(COVERPKG) -coverprofile=$(COVERPROFILE) github.com/Rocketable/platform/cmd/rocketclaw/... ./...; \
	if ! command -v jj >/dev/null 2>&1 || ! jj workspace root >/dev/null 2>&1; then \
		current=$$(go tool cover -func="$(COVERPROFILE)" | awk '/^total:/ { gsub("%", "", $$3); print $$3 }'); \
		printf "coverage: %s%%\n" "$$current"; \
		exit 0; \
	fi; \
	tmp_dir=.tmp; \
	mkdir -p "$$tmp_dir"; \
	base_revision=$$(jj log -r @- --no-graph -T commit_id); \
	makefile_checksum=$$(cksum Makefile); makefile_checksum=$${makefile_checksum%% *}; \
	coverage_checksum=$$(printf '%s' '$(COVERPKG)' | cksum); coverage_checksum=$${coverage_checksum%% *}; \
	cache_file="$$tmp_dir/rocketclaw-coverage-base-$$base_revision-$$(go env GOVERSION)-$$(go env GOOS)-$$(go env GOARCH)-$$makefile_checksum-$$coverage_checksum"; \
	if [ -f "$$cache_file" ]; then \
		IFS= read -r base < "$$cache_file"; \
		printf "coverage baseline cache hit: %s%%\n" "$$base"; \
	else \
		base_dir=$$(mktemp -d "$$tmp_dir/rocketclaw-coverage-base-XXXXXX"); \
		rmdir "$$base_dir"; \
		jj --quiet workspace add --revision @- "$$base_dir"; \
		(cd "$$base_dir" && go mod tidy && go mod vendor); \
		(cd "$$base_dir/internal/rocketclaw" && go test -race -count 1 -covermode=atomic -coverpkg=$(COVERPKG) -coverprofile=$(COVERPROFILE) github.com/Rocketable/platform/cmd/rocketclaw/... ./...); \
		base=$$(cd "$$base_dir/internal/rocketclaw" && go tool cover -func="$(COVERPROFILE)" | awk '/^total:/ { gsub("%", "", $$3); print $$3 }'); \
		printf '%s\n' "$$base" > "$$cache_file"; \
		printf "coverage baseline cache miss: stored %s%%\n" "$$base"; \
	fi; \
	current=$$(go tool cover -func="$(COVERPROFILE)" | awk '/^total:/ { gsub("%", "", $$3); print $$3 }'); \
	[ -n "$$base" ] && [ -n "$$current" ] || { printf "coverage budget failed: unable to read coverage totals\n"; exit 1; }; \
	awk -v base="$$base" -v current="$$current" -v stable_at="$(COVERAGE_STABLE_AT)" 'BEGIN { if (current + 0 < stable_at + 0 && current + 0 < base + 0) { printf "coverage budget failed (coverage must not decrease until %.1f%%): current %.1f%%, base %.1f%%\n", stable_at, current, base; exit 1 } if (current + 0 < stable_at + 0) { printf "coverage budget ok: current %.1f%% >= base %.1f%%\n", current, base; exit 0 } printf "coverage budget ok: current %.1f%% >= stable %.1f%%\n", current, stable_at }'

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
