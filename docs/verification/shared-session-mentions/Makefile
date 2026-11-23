SHELL := /bin/bash
ROOT := $(abspath ../../..)
WORK := $(ROOT)/.tmp/verification/shared-session-mentions
MODEL := SharedMentions.tla
TLA_VERSION := 1.8.0
TLA_SHA256 := fdffef247f563ef6edbfd44958d2a82db79e7564e07cb673262d896353fa6713
TOOLS := $(WORK)/tla2tools-$(TLA_VERSION).jar
JAVA := java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC := $(JAVA) -Xmx1g -cp $(TOOLS) tlc2.TLC -workers 1 -noGenerateSpecTE
NEGATIVE := early trust-copy wrong-person stale-view overwrite
EXPECTED_early := MarkAfterAdmission
EXPECTED_trust-copy := MarkAfterAdmission
EXPECTED_wrong-person := MarkOnlySelected
EXPECTED_stale-view := ViewRequiresAccess
EXPECTED_overwrite := ActivityKeepsMarks

.PHONY: check tools translate normal $(NEGATIVE)
check: normal $(NEGATIVE)

$(TOOLS):
	mkdir -p $(WORK)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar

tools: $(TOOLS)
	@mkdir -p $(WORK)/java $(WORK)/translate
	@echo '$(TLA_SHA256)  $(TOOLS)' | shasum -a 256 -c -

translate: tools
	cp $(MODEL) $(WORK)/translate/$(MODEL)
	cd $(WORK)/translate && $(JAVA) -cp $(TOOLS) pcal.trans -nocfg $(MODEL)
	cp $(WORK)/translate/$(MODEL) $(MODEL)

normal: translate
	cd $(WORK) && $(TLC) -metadir $@ -config $(CURDIR)/$@.cfg $(CURDIR)/$(MODEL) > $@.log 2>&1; status=$$?; \
	cat $@.log; test $$status -eq 0 && grep -Fq 'Model checking completed. No error has been found.' $@.log

$(NEGATIVE): translate
	cd $(WORK) && $(TLC) -metadir $@ -config $(CURDIR)/$@.cfg $(CURDIR)/$(MODEL) > $@.log 2>&1; status=$$?; \
	{ test $$status -eq 12 || test $$status -eq 13; } && grep -F 'Error: Invariant $(EXPECTED_$@) is violated.' $@.log || { cat $@.log; exit 1; }
