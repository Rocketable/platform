generate:
	mkdir -p internal/rocketclaw/frontend/rpc/proto
	cp proto/web.proto internal/rocketclaw/frontend/rpc/proto/web.proto
	go generate ./cmd/... ./internal/...
	cd web; $(MAKE) generate

test: generate
	cd internal/rocketclaw; $(MAKE) test
	cd internal/rocketcode; $(MAKE) test
	cd internal/funneld; $(MAKE) test
	cd web; $(MAKE) test

lint: generate
	cd internal/rocketclaw; $(MAKE) lint
	cd internal/rocketcode; $(MAKE) lint
	cd internal/funneld; $(MAKE) lint
	cd web; $(MAKE) lint

build: generate
	cd internal/rocketclaw; $(MAKE) build
	cd internal/funneld; $(MAKE) build
	cd web; $(MAKE) build

cloc:
	cd internal/rocketclaw; $(MAKE) cloc
	cd internal/rocketcode; $(MAKE) cloc
	cd internal/funneld; $(MAKE) cloc
	cd web; $(MAKE) cloc

check-cloc-budget:
	cd internal/rocketclaw; $(MAKE) check-cloc-budget
	cd internal/rocketcode; $(MAKE) check-cloc-budget
	cd internal/funneld; $(MAKE) check-cloc-budget
	cd web; $(MAKE) check-cloc-budget

deploy:
	cd internal/rocketclaw; $(MAKE) deploy
