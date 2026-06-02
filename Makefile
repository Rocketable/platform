test:
	cd internal/rocketclaw; $(MAKE) test
	cd internal/rocketcode; $(MAKE) test

lint:
	cd internal/rocketclaw; $(MAKE) lint
	cd internal/rocketcode; $(MAKE) lint

build:
	cd internal/rocketclaw; $(MAKE) build
	cd internal/rocketcode; $(MAKE) build

deploy:
	cd internal/rocketclaw; $(MAKE) deploy
