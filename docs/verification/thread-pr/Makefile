# Canonical model/configs live here. All tools, logs, traces and scratch copies
# live under this workspace's root .tmp, never docs/.tmp or system temp.
SHELL := /bin/bash
ROOT := $(abspath ../../..)
WORK := $(ROOT)/.tmp/run/thread-pr-model
MODEL := $(CURDIR)/ThreadPR.tla
TLA_VERSION := 1.8.0
TLA_SHA256 := e1f11b75c4b5bc4478ba0ac5cb6ae28e40a01b0016bc33e0aa97da22dfdbcd7f
TOOLS := $(WORK)/tla2tools-$(TLA_VERSION).jar
JAVA := java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC := $(JAVA) -Xmx1g -cp $(TOOLS) tlc2.TLC -workers 1 -noGenerateSpecTE
PASS := discovery reader shell source-guards
NEGATIVE := no-abort stale-navigation no-post own-checkout drop-history abort-create skip-delete source-change source-patch aba browser-loss delete-failure witness-cancel
EXPECTED_no-abort := Invariant NoCancelledCommit is violated.
EXPECTED_stale-navigation := Invariant NoStaleCommit is violated.
EXPECTED_no-post := Invariant PatchMatchesMetadata is violated.
EXPECTED_own-checkout := Invariant OnlyProvenCreateOwned is violated.
EXPECTED_drop-history := Invariant HistorySurvivesCheckoutFailure is violated.
EXPECTED_source-change := Invariant DisplayedContextMatches is violated.
EXPECTED_source-patch := Invariant PatchMatchesMetadata is violated.
EXPECTED_aba := Invariant PatchMatchesMetadata is violated.
EXPECTED_witness-cancel := Invariant NeverCancelled is violated.
EXPECTED_abort-create := Temporal property CreatedShellEventuallyDeleted was violated.
EXPECTED_skip-delete := Temporal property KnownShellEventuallyDeleted was violated.
EXPECTED_browser-loss := Temporal property KnownShellEventuallyDeleted was violated.
EXPECTED_delete-failure := Temporal property KnownShellEventuallyDeleted was violated.

.NOTPARALLEL:
.PHONY: all check translate tools $(PASS) $(NEGATIVE)
all: check
check: $(PASS) $(NEGATIVE)

tools: $(TOOLS)
	@mkdir -p $(WORK)/java
	@echo '$(TLA_SHA256)  $(TOOLS)' | shasum -a 256 -c -

$(TOOLS):
	mkdir -p $(WORK)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar

# Translate a scratch copy so pcal's .old backup never appears beside the model.
translate: tools
	@mkdir -p $(WORK)/translate
	cp $(MODEL) $(WORK)/translate/ThreadPR.tla
	@cd $(WORK)/translate && $(JAVA) -cp $(TOOLS) pcal.trans -nocfg ThreadPR.tla > $(WORK)/translate.log 2>&1
	@cat $(WORK)/translate.log
	cp $(WORK)/translate/ThreadPR.tla $(MODEL)

$(PASS): translate
	@mkdir -p $(WORK)/$@
	@cd $(WORK) && $(TLC) -metadir $(WORK)/$@ -config $(CURDIR)/$@.cfg $(MODEL) > $(WORK)/$@.log 2>&1; status=$$?; \
	cat $(WORK)/$@.log; test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

# A parser/tool failure is NOT an expected counterexample. Require TLC's named
# violation and its nonzero invariant/temporal exit status, retaining full trace.
$(NEGATIVE): translate
	@mkdir -p $(WORK)/$@
	@cd $(WORK) && $(TLC) -metadir $(WORK)/$@ -config $(CURDIR)/$@.cfg $(MODEL) > $(WORK)/$@.log 2>&1; status=$$?; \
	test $$status -eq 12 -o $$status -eq 13 && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|states generated|distinct states|depth of' $(WORK)/$@.log; echo '$@: expected counterexample confirmed'
