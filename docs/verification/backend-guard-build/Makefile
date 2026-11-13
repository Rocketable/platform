SHELL := /bin/bash
ROOT := $(abspath ../../..)
WORK := $(ROOT)/.tmp/backend-guard-build-model
TOOLS := $(WORK)/tla2tools-1.8.0.jar
MODEL := $(CURDIR)/GuardBuild.tla
JAVA := java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC := $(JAVA) -Xmx256m -cp $(TOOLS) tlc2.TLC -workers 1 -noGenerateSpecTE
PASS := shared
NEGATIVE := no-lock early-cleanup
EXPECTED_no-lock := Invariant AtMostOneSuccessfulBuild is violated.
EXPECTED_early-cleanup := Invariant CleanupAfterUse is violated.

.NOTPARALLEL:
.PHONY: all check tools translate $(PASS) $(NEGATIVE)
all: check
check: $(PASS) $(NEGATIVE)

$(TOOLS):
	mkdir -p $(WORK)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v1.8.0/tla2tools.jar

tools: $(TOOLS)
	@mkdir -p $(WORK)/java
	@echo 'e1f11b75c4b5bc4478ba0ac5cb6ae28e40a01b0016bc33e0aa97da22dfdbcd7f  $(TOOLS)' | shasum -a 256 -c -

translate: tools
	@mkdir -p $(WORK)/translate
	cp $(MODEL) $(WORK)/translate/GuardBuild.tla
	@cd $(WORK)/translate && $(JAVA) -cp $(TOOLS) pcal.trans -nocfg GuardBuild.tla > $(WORK)/translate.log 2>&1
	@cat $(WORK)/translate.log
	cp $(WORK)/translate/GuardBuild.tla $(MODEL)

$(PASS): translate
	@mkdir -p $(WORK)/$@
	@cd $(WORK) && $(TLC) -metadir $(WORK)/$@ -config $(CURDIR)/$@.cfg $(MODEL) > $(WORK)/$@.log 2>&1; status=$$?; \
	cat $(WORK)/$@.log; test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

$(NEGATIVE): translate
	@mkdir -p $(WORK)/$@
	@cd $(WORK) && $(TLC) -metadir $(WORK)/$@ -config $(CURDIR)/$@.cfg $(MODEL) > $(WORK)/$@.log 2>&1; status=$$?; \
	test $$status -eq 12 -o $$status -eq 13 && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|states generated|distinct states|depth of' $(WORK)/$@.log
