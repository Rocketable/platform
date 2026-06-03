test:
	cd internal/rocketclaw; $(MAKE) test
	cd internal/rocketcode; $(MAKE) test
	cd internal/interviewd; $(MAKE) test

lint:
	cd internal/rocketclaw; $(MAKE) lint
	cd internal/rocketcode; $(MAKE) lint
	cd internal/interviewd; $(MAKE) lint

build:
	cd internal/rocketclaw; $(MAKE) build
	cd internal/rocketcode; $(MAKE) build
	cd internal/interviewd; $(MAKE) build

cloc:
	cd internal/rocketclaw; $(MAKE) cloc
	cd internal/rocketcode; $(MAKE) cloc
	cd internal/interviewd; $(MAKE) cloc

check-cloc-budget:
	cd internal/rocketclaw; $(MAKE) check-cloc-budget
	cd internal/rocketcode; $(MAKE) check-cloc-budget
	cd internal/interviewd; $(MAKE) check-cloc-budget

deploy:
	cd internal/rocketclaw; $(MAKE) deploy
