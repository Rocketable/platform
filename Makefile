test:
	cd internal/rocketclaw; $(MAKE) test
	cd internal/rocketcode; $(MAKE) test
	cd internal/funneld; $(MAKE) test

lint:
	cd internal/rocketclaw; $(MAKE) lint
	cd internal/rocketcode; $(MAKE) lint
	cd internal/funneld; $(MAKE) lint

build:
	cd internal/rocketclaw; $(MAKE) build
	cd internal/rocketcode; $(MAKE) build
	cd internal/funneld; $(MAKE) build

cloc:
	cd internal/rocketclaw; $(MAKE) cloc
	cd internal/rocketcode; $(MAKE) cloc
	cd internal/funneld; $(MAKE) cloc

check-cloc-budget:
	cd internal/rocketclaw; $(MAKE) check-cloc-budget
	cd internal/rocketcode; $(MAKE) check-cloc-budget
	cd internal/funneld; $(MAKE) check-cloc-budget

deploy:
	cd internal/rocketclaw; $(MAKE) deploy
