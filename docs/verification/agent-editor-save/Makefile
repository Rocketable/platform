# Canonical model/configs live here. All tools, logs, traces and scratch copies
# live under this workspace's root .tmp, never docs/.tmp or system temp.
SHELL := /bin/bash
ROOT := $(abspath ../../..)
WORK := $(ROOT)/.tmp/run/agent-editor-save
MODEL := $(CURDIR)/AgentEditorSave.tla
TLA_VERSION := 1.8.0
TLA_SHA256 := e1f11b75c4b5bc4478ba0ac5cb6ae28e40a01b0016bc33e0aa97da22dfdbcd7f
TOOLS := $(WORK)/tla2tools-$(TLA_VERSION).jar
JAVA := java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC := $(JAVA) -Xmx4g -cp $(TOOLS) tlc2.TLC -workers 2 -noGenerateSpecTE
PASS := workspace overlay-gh overlay-no-gh overlay-two-users
NEGATIVE := disconnect-workspace disconnect-overlay stale-check-end edit-keeps-live \
	claim-reply-lost write-race merge-unnoticed \
	plan-double-save plan-overlay-double-save plan-invalid-live plan-broken-start plan-stale-overwrite \
	plan-overlay-stale-push plan-mixed-live plan-false-live plan-undo-clobber plan-stuck-saving plan-stuck-waiting \
	reach-live reach-stale-review reach-crash-mid-save reach-invalid-draft reach-reload-fail reach-live-edit \
	reach-cut-save reach-cut-push reach-pr-live reach-push-retry reach-stale-finish
EXPECTED_disconnect-workspace := Temporal property SavingResolves was violated.
EXPECTED_disconnect-overlay := Temporal property SavingResolves was violated.
EXPECTED_stale-check-end := Invariant NoStaleCheckEnd is violated.
EXPECTED_edit-keeps-live := Invariant EditedNotLive is violated.
EXPECTED_claim-reply-lost := Temporal property SavingResolves was violated.
EXPECTED_write-race := Invariant StaleNeedsReview is violated.
EXPECTED_merge-unnoticed := Temporal property MergeResolves was violated.
EXPECTED_plan-double-save := Invariant SaveExclusive is violated.
EXPECTED_plan-overlay-double-save := Invariant SaveExclusive is violated.
EXPECTED_plan-invalid-live := Invariant NoInvalidLive is violated.
EXPECTED_plan-broken-start := Invariant StartupOK is violated.
EXPECTED_plan-stale-overwrite := Invariant StaleNeedsReview is violated.
EXPECTED_plan-overlay-stale-push := Invariant StaleNeedsReview is violated.
EXPECTED_plan-mixed-live := Invariant NoMixedLive is violated.
EXPECTED_plan-false-live := Invariant HonestLive is violated.
EXPECTED_plan-undo-clobber := Invariant NoUndoClobber is violated.
EXPECTED_plan-stuck-saving := Temporal property SavingResolves was violated.
EXPECTED_plan-stuck-waiting := Temporal property WaitingResolves was violated.
EXPECTED_reach-live := Invariant ProbeNotLive is violated.
EXPECTED_reach-stale-review := Invariant ProbeNoStaleCatch is violated.
EXPECTED_reach-crash-mid-save := Invariant ProbeNoCrashSave is violated.
EXPECTED_reach-invalid-draft := Invariant ProbeNoInvalidDraft is violated.
EXPECTED_reach-reload-fail := Invariant ProbeNoReloadFailEditing is violated.
EXPECTED_reach-live-edit := Invariant ProbeNoLiveEdit is violated.
EXPECTED_reach-cut-save := Invariant ProbeNoCutSave is violated.
EXPECTED_reach-cut-push := Invariant ProbeNoCutPush is violated.
EXPECTED_reach-pr-live := Invariant ProbeNoPrLive is violated.
EXPECTED_reach-push-retry := Invariant ProbeNoRetry is violated.
EXPECTED_reach-stale-finish := Invariant ProbeNoStaleFinish is violated.

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
	cp $(MODEL) $(WORK)/translate/AgentEditorSave.tla
	@cd $(WORK)/translate && $(JAVA) -cp $(TOOLS) pcal.trans -nocfg AgentEditorSave.tla > $(WORK)/translate.log 2>&1
	@cat $(WORK)/translate.log
	cp $(WORK)/translate/AgentEditorSave.tla $(MODEL)

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
