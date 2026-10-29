# generate rewrites vendor/ and the mocks, so it finishes before
# the rocketclaw tests overlap with the funneld checks.
test:
	cd internal/rocketclaw; $(MAKE) generate lint
	$(MAKE) -j 2 test-rocketclaw test-others

test-rocketclaw:
	cd internal/rocketclaw; $(MAKE) check-code-metrics

test-others:
	cd internal/funneld; $(MAKE) test

lint:
	cd internal/rocketclaw; $(MAKE) lint
	cd internal/funneld; $(MAKE) lint

build:
	cd internal/rocketclaw; $(MAKE) build
	cd internal/funneld; $(MAKE) build

cloc:
	cd internal/rocketclaw; $(MAKE) cloc
	cd internal/funneld; $(MAKE) cloc
	cd internal/rocketclaw/web; $(MAKE) cloc
	cd internal/rocketclaw/oc; $(MAKE) cloc

check-cloc-budget:
	cd internal/rocketclaw; $(MAKE) check-cloc-budget
	cd internal/funneld; $(MAKE) check-cloc-budget
	cd internal/rocketclaw/web; $(MAKE) check-cloc-budget

deploy:
	cd internal/rocketclaw; $(MAKE) deploy
