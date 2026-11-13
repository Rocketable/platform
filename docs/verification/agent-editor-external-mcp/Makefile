# Canonical model/configs live here. All tools, logs, traces and scratch copies
# live under this workspace's root .tmp, never docs/.tmp or system temp.
SHELL := /bin/bash
ROOT := $(abspath ../../..)
WORK := $(ROOT)/.tmp/run/agent-editor-external-mcp
MODEL := $(CURDIR)/AgentEditorExternalMCP.tla
TLA_VERSION := 1.8.0
TLA_SHA256 := e1f11b75c4b5bc4478ba0ac5cb6ae28e40a01b0016bc33e0aa97da22dfdbcd7f
TOOLS := $(WORK)/tla2tools-$(TLA_VERSION).jar
JAVA := java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC := $(JAVA) -Xmx4g -cp $(TOOLS) tlc2.TLC -workers 2 -noGenerateSpecTE
PASS := as-built two-callers
NEGATIVE := queue-steer queue-delete queue-delete-later queue-reorder panel-unordered raw-lock-check cid-before-pairs \
	generic-prompt notice-overtakes clash-unlocked reserved-key stale-suggestion peer-overwrite \
	no-conflict-check early-conflict-check mode-toggle mode-toggle-metadata no-sticky-check page-clears-only \
	sticky-uncaptured live-run freeze-draftguard chatfreeze-realrule chatfreeze-modehonest \
	probe-mcp probe-second probe-restart probe-newkey probe-live probe-renamed-key
EXPECTED_queue-steer := Invariant InboundHonest is violated.
EXPECTED_queue-delete := Invariant LocksAgree is violated.
EXPECTED_queue-delete-later := Invariant InboundHonest is violated.
EXPECTED_queue-reorder := Invariant InboundHonest is violated.
EXPECTED_panel-unordered := Invariant NoOwnRegress is violated.
EXPECTED_raw-lock-check := Invariant InboundHonest is violated.
EXPECTED_cid-before-pairs := Invariant RealRule is violated.
EXPECTED_generic-prompt := Invariant NoHybridTurn is violated.
EXPECTED_notice-overtakes := Invariant NoOwnRegress is violated.
EXPECTED_clash-unlocked := Invariant InboundHonest is violated.
EXPECTED_reserved-key := Invariant InboundHonest is violated.
EXPECTED_stale-suggestion := Invariant MetaVarSuggestion is violated.
EXPECTED_peer-overwrite := Action property NoSwitchOverwrite is violated.
EXPECTED_no-conflict-check := Invariant InboundHonest is violated.
EXPECTED_early-conflict-check := Invariant InboundHonest is violated.
EXPECTED_mode-toggle := Invariant FrozenPerChat is violated.
EXPECTED_mode-toggle-metadata := Invariant NoHybridTurn is violated.
EXPECTED_no-sticky-check := Invariant OnlyStickyOff is violated.
EXPECTED_page-clears-only := Invariant OnlyStickyOff is violated.
EXPECTED_sticky-uncaptured := Invariant OnlyStickyOff is violated.
EXPECTED_live-run := Invariant OnlyStickyOff is violated.
EXPECTED_freeze-draftguard := Invariant OnlyStickyOff is violated.
EXPECTED_chatfreeze-realrule := Invariant RealRule is violated.
EXPECTED_chatfreeze-modehonest := Invariant ModeHonest is violated.
EXPECTED_probe-mcp := Invariant NoMCPTurn is violated.
EXPECTED_probe-second := Invariant NoSecondMCPMessage is violated.
EXPECTED_probe-restart := Invariant NoMCPInRestartedChat is violated.
EXPECTED_probe-newkey := Invariant MetaFrozenInMCPChat is violated.
EXPECTED_probe-live := Invariant NoLiveMCPTurn is violated.
EXPECTED_probe-renamed-key := Invariant NoRenamedLockedKey is violated.

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
	cp $(MODEL) $(WORK)/translate/AgentEditorExternalMCP.tla
	@cd $(WORK)/translate && $(JAVA) -cp $(TOOLS) pcal.trans -nocfg AgentEditorExternalMCP.tla > $(WORK)/translate.log 2>&1
	@cat $(WORK)/translate.log
	cp $(WORK)/translate/AgentEditorExternalMCP.tla $(MODEL)

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
