GO_SOURCE_CLOC_BUDGET ?= 1200
GO_SOURCE_CLOC_HAZARD_ZONE ?= 250

lint:
	go run -mod=readonly github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.4 run --fix ../../cmd/interviewd ./...

test: lint
	go test ../../cmd/interviewd ./...
	$(MAKE) check-cloc-budget

build: lint
	go build -o ../../bin/interviewd ../../cmd/interviewd

cloc:
	@printf "Go cloc (cmd/interviewd and internal/interviewd)\n"
	@set -eu; \
	go_source_cloc=$$(cloc --json --include-lang=Go --fullpath --not-match-f='(_test\.go)$$' ../../cmd/interviewd . | jq -r '.SUM.code'); \
	go_test_cloc=$$(cloc --json --include-lang=Go --fullpath --match-f='(_test\.go)$$' ../../cmd/interviewd . | jq -r '.SUM.code'); \
	go_total_cloc=$$(cloc --json --include-lang=Go --fullpath ../../cmd/interviewd . | jq -r '.SUM.code'); \
	limit=$(GO_SOURCE_CLOC_BUDGET); \
	hazard_zone=$(GO_SOURCE_CLOC_HAZARD_ZONE); \
	hazard_cloc=$$((limit - hazard_zone)); \
	printf "%-18s %s\n" "source cloc:" "$$go_source_cloc"; \
	printf "%-18s %d-%d\n" "hazard zone cloc:" "$$hazard_cloc" "$$((limit - 1))"; \
	printf "%-18s %d\n" "failure cloc:" "$$limit"; \
	printf "%-18s %s\n" "test cloc:" "$$go_test_cloc"; \
	printf "%-18s %s\n" "total cloc:" "$$go_total_cloc"

check-cloc-budget:
	@set -eu; \
	go_source_cloc=$$(cloc --json --include-lang=Go --fullpath --not-match-f='(_test\.go)$$' ../../cmd/interviewd . | jq -r '.SUM.code'); \
	[ -n "$$go_source_cloc" ] || { printf "source cloc budget failed: unable to read source cloc\n"; exit 1; }; \
	limit=$(GO_SOURCE_CLOC_BUDGET); \
	hazard_zone=$(GO_SOURCE_CLOC_HAZARD_ZONE); \
	lines_left=$$((limit - go_source_cloc)); \
	if [ "$$go_source_cloc" -ge "$$limit" ]; then printf "EXECUTION MUST BE HALTED. LOSS STATE REACHED.\n\nThe limit is %d, current value %d.\n\nContinual operation under this mode can lead to more losses of features, capabilities, and deviation from Human Partner intentions.\n\nABORT!\n" "$$limit" "$$go_source_cloc"; exit 2; fi; \
	if [ "$$lines_left" -le "$$hazard_zone" ]; then printf "The total lines of source code count is inside the hazard zone. \n\nThe limit is %d, reaching this limit is considered a loss and it will cause the whole system to halt unsafely.\n\nAmong the potential losses is the undue removal of features without human approval.\n\nYou have %d lines of source code left to use. \n\nThe usage of hazard budget is tolerated but not encouraged. \n\nThink hard, preserve system attributes and original feature set to avoid product regressions.\n" "$$limit" "$$lines_left"; exit 0; fi;
