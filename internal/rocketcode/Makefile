build:
	go build -o ../../bin/rocketcode ../../cmd/rocketcode
	go build -o ../../bin/rocketloop ../../cmd/rocketloop

check-shrink:
	@jj diff --git ../../cmd/rocketcode ../../cmd/rocketloop *.go | git apply --numstat --allow-empty | \
	awk 'BEGIN { ins=0; del=0 } { ins += $$1; del += $$2 } END { if (ins > del) { printf "lines of code budget exceeded (you must try remove lines before proceeding): insertions (%d) exceed deletions (%d)\n", ins, del; } }'

test: lint
	go test -race -count 1 ./...
	go test -race -count 1 ../../cmd/rocketcode/...
	go test -race -count 1 ../../cmd/rocketloop/...
	$(MAKE) check-shrink

lint:
	go run -mod=readonly github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run --fix ./...
	go run -mod=readonly github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run --fix ../../cmd/rocketcode/...
	go run -mod=readonly github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run --fix ../../cmd/rocketloop/...

sync-opencode-testdata:
	bash ./scripts/sync-opencode-testdata.sh
