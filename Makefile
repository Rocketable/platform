

test:
	$(MAKE) -j 2 test-rocketclaw test-others

test-rocketclaw:
	cd internal/rocketclaw; $(MAKE) test

test-others:
	cd internal/funneld; $(MAKE) test

lint:
	cd internal/rocketclaw; $(MAKE) lint
	cd internal/funneld; $(MAKE) lint

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
