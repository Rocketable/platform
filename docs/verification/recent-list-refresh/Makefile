SHELL := /bin/bash
ROOT := $(abspath ../../..)
WORK := $(ROOT)/.tmp/run/recent-list-refresh-model
MODEL := RecentListRefresh.tla
TLA_VERSION := 1.8.0
TLA_SHA256 := 67f9575c3b9ef7a0787b1bded34812862ffe79d711298e5a75b1a77720b19928
TOOLS := $(WORK)/tla2tools-$(TLA_VERSION).jar
JAVA := java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC -Xmx512m -cp $(TOOLS)
TLC := $(JAVA) tlc2.TLC -workers 1 -noGenerateSpecTE
NEGATIVE := no-repair same-notify ignore-ordinary ignore-refusal ignore-mutation stale-stamp
EXPECTED_no-repair := VisitRecovered
EXPECTED_same-notify := VisitRecovered
EXPECTED_ignore-ordinary := NoStaleRepair
EXPECTED_ignore-refusal := NoStaleRepair
EXPECTED_ignore-mutation := NoStaleRepair
EXPECTED_stale-stamp := StampPreserved

.NOTPARALLEL:
.PHONY: all check translate tools positive $(NEGATIVE)
all: check
check: positive $(NEGATIVE)

tools: $(TOOLS)
	mkdir -p $(WORK)/java
	@echo '$(TLA_SHA256)  $(TOOLS)' | shasum -a 256 -c -

$(TOOLS):
	mkdir -p $(WORK)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar

translate: tools
	cp $(MODEL) $(WORK)/$(MODEL)
	cd $(WORK) && $(JAVA) pcal.trans -nocfg $(MODEL) > translate.log
	cp $(WORK)/$(MODEL) $(MODEL)

positive: translate
	@cd $(WORK) && $(TLC) -metadir positive -config $(CURDIR)/check.cfg $(MODEL) > positive.log 2>&1; status=$$?; \
	cat $(WORK)/positive.log; test $$status -eq 0 && grep -Fq 'Model checking completed. No error has been found.' $(WORK)/positive.log

$(NEGATIVE): translate
	@cd $(WORK) && $(TLC) -metadir $@ -config $(CURDIR)/$@.cfg $(MODEL) > $@.log 2>&1; status=$$?; \
	test $$status -eq 12 -o $$status -eq 13 && grep -Fq 'Error: Invariant $(EXPECTED_$@) is violated.' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|states generated|distinct states|depth of' $(WORK)/$@.log
