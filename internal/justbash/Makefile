TOOLS := $(CURDIR)/../../.tmp/bin
STAGE := $(CURDIR)/../../.tmp/justbash-runtime

lint:
	go run -mod=readonly github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run --fix ./...
	cd wrapper && bun install --frozen-lockfile && bunx tsc --noEmit

test: lint
	go test -race -count 1 ./...

# generate rebuilds the protocol code. It needs protoc on PATH; CI checks only the runtime tree.
generate:
	cd wrapper && bun install --frozen-lockfile
	GOBIN=$(TOOLS) go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
	GOBIN=$(TOOLS) go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
	protoc -I proto \
		--plugin=protoc-gen-go=$(TOOLS)/protoc-gen-go --go_out=justbashpb --go_opt=paths=source_relative \
		--plugin=protoc-gen-go-grpc=$(TOOLS)/protoc-gen-go-grpc --go-grpc_out=justbashpb --go-grpc_opt=paths=source_relative,require_unimplemented_servers=false \
		--plugin=protoc-gen-es=wrapper/node_modules/.bin/protoc-gen-es --es_out=wrapper/src/gen --es_opt=target=ts \
		proto/justbash.proto

# runtime rebuilds the embedded tree: the bundled wrapper plus just-bash's pruned production dependencies.
runtime:
	cd wrapper && bun install --frozen-lockfile
	rm -rf $(STAGE) && mkdir -p $(STAGE)
	cp wrapper/package.json wrapper/bun.lock $(STAGE)/
	cd $(STAGE) && bun install --frozen-lockfile --production --omit optional --ignore-scripts
	cd wrapper && bun build src/main.ts --target=node --format=esm --external just-bash --outfile=$(STAGE)/wrapper.mjs
	cd wrapper && bun scripts/prune.ts $(STAGE)
	rm -rf runtime && mv $(STAGE) runtime

cloc:
	@printf "cloc (internal/justbash, no budget)\n"
	@set -eu; \
	count() { cloc --json --fullpath --not-match-d='(node_modules|runtime|gen|justbashpb)' "$$@" | jq -r '.SUM.code // 0'; }; \
	printf "%-18s %s\n" "go source cloc:" "$$(count --include-lang=Go --not-match-f='(_test\.go)$$' .)"; \
	printf "%-18s %s\n" "go test cloc:" "$$(count --include-lang=Go --match-f='(_test\.go)$$' .)"; \
	printf "%-18s %s\n" "wrapper cloc:" "$$(count --include-lang=TypeScript wrapper)"

.PHONY: lint test generate runtime cloc
