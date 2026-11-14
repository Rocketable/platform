# Canonical model/configs live here. All tools, logs, traces and scratch copies
# live under this workspace's root .tmp, never docs/.tmp or system temp.
SHELL := /bin/bash
ROOT := $(abspath ../../..)
WORK := $(ROOT)/.tmp/run/agent-editor-test-turns
MODEL := $(CURDIR)/AgentEditorTestTurns.tla
TLA_VERSION := 1.8.0
TLA_SHA256 := e1f11b75c4b5bc4478ba0ac5cb6ae28e40a01b0016bc33e0aa97da22dfdbcd7f
TOOLS := $(WORK)/tla2tools-$(TLA_VERSION).jar
JAVA := java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC := $(JAVA) -Xmx4g -cp $(TOOLS) tlc2.TLC -workers 2 -lncheck final -noGenerateSpecTE
PASS := as-built as-built-typing as-built-liveness
NEGATIVE := liveness-unfair witness-drain-wait \
	replay-skips-queued replay-send-during-restart replay-stale-lists replay-fresh-reads replay-teammate-send \
	stash-without-record refused-replay-switches-chat \
	wake-disk wake-row wake-sub-lineage typed-no-flush typed-flush-send-only \
	wake-copy-fanout wake-copy-sub wake-copy-batch \
	plan-live-disk plan-label-at-send capture-parent-start pre-feature-disk restart-in-place \
	positional roster-disk torn-save parent-framing
EXPECTED_liveness-unfair := Temporal property EveryColumnAnswers was violated.
EXPECTED_witness-drain-wait := Invariant NeverWaitsForKeystrokes is violated.
EXPECTED_replay-skips-queued := Invariant ReplayAll is violated.
EXPECTED_replay-send-during-restart := Invariant ReplayAll is violated.
EXPECTED_replay-stale-lists := Invariant ReplayAll is violated.
EXPECTED_replay-fresh-reads := Invariant ReplayAll is violated.
EXPECTED_replay-teammate-send := Invariant ReplayAll is violated.
EXPECTED_stash-without-record := Invariant ReplayAll is violated.
EXPECTED_refused-replay-switches-chat := Invariant ReplayAll is violated.
EXPECTED_wake-disk := Invariant WakeUsesCapture is violated.
EXPECTED_wake-row := Invariant WakeRowHonest is violated.
EXPECTED_wake-sub-lineage := Invariant SubWakeUsesCapture is violated.
EXPECTED_typed-no-flush := Invariant SendIncludesTyped is violated.
EXPECTED_typed-flush-send-only := Invariant SendIncludesTyped is violated.
EXPECTED_wake-copy-fanout := Invariant NoShadowWakeTurn is violated.
EXPECTED_wake-copy-sub := Invariant WakeUsesCapture is violated.
EXPECTED_wake-copy-batch := Invariant WakeOneCapture is violated.
EXPECTED_plan-live-disk := Invariant VersionAtSendLive is violated.
EXPECTED_plan-label-at-send := Invariant LabelTruthful is violated.
EXPECTED_capture-parent-start := Invariant VersionAtSendDraft is violated.
EXPECTED_pre-feature-disk := Invariant VersionAtSendDraft is violated.
EXPECTED_restart-in-place := Invariant RestartIsolation is violated.
EXPECTED_positional := Invariant AlignedPerMessage is violated.
EXPECTED_roster-disk := Invariant RosterMatchesDraft is violated.
EXPECTED_torn-save := Invariant LabelTruthful is violated.
EXPECTED_parent-framing := Invariant FramingOwn is violated.

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
	cp $(MODEL) $(WORK)/translate/AgentEditorTestTurns.tla
	@cd $(WORK)/translate && $(JAVA) -cp $(TOOLS) pcal.trans -nocfg AgentEditorTestTurns.tla > $(WORK)/translate.log 2>&1
	@cat $(WORK)/translate.log
	cp $(WORK)/translate/AgentEditorTestTurns.tla $(MODEL)

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
