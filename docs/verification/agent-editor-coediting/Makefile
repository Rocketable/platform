# Canonical model/configs live here. All tools, logs, traces and scratch copies
# live under this workspace's root .tmp, never docs/.tmp or system temp.
SHELL := /bin/bash
ROOT := $(abspath ../../..)
WORK := $(ROOT)/.tmp/run/agent-editor-coediting
MODEL := $(CURDIR)/AgentEditorCoediting.tla
TLA_VERSION := 1.8.0
TLA_SHA256 := e1f11b75c4b5bc4478ba0ac5cb6ae28e40a01b0016bc33e0aa97da22dfdbcd7f
TOOLS := $(WORK)/tla2tools-$(TLA_VERSION).jar
JAVA := java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC := $(JAVA) -Xmx4g -cp $(TOOLS) tlc2.TLC -workers 2 -noGenerateSpecTE
PASS := as-built as-built-wide file-both file-three-edits assistant liveness
NEGATIVE := diff-cap-lost-delete diff-cap-reorder file-stale-base-revert file-resends-form-write reject-drops-queue file-ackrev-rebase witness-refusal-text-behind witness-change-back witness-from-pending-write witness-late-accept plan-widen plan-diverge plan-clip plan-delete-wins plan-single-splice plan-file-prefix-suffix plan-file-prefix-suffix-lost plan-file-while-busy plan-remote-set-wins
EXPECTED_diff-cap-lost-delete := Invariant NoLostEdit is violated.
EXPECTED_diff-cap-reorder := Invariant OrderPreserved is violated.
EXPECTED_file-stale-base-revert := Invariant OwnLastWrite is violated.
EXPECTED_file-resends-form-write := Invariant FileKeepsOthers is violated.
EXPECTED_reject-drops-queue := Invariant NoDroppedEdit is violated.
EXPECTED_file-ackrev-rebase := Invariant FileKeepsOthers is violated.
EXPECTED_witness-refusal-text-behind := Invariant NoRefusalWithTextBehind is violated.
EXPECTED_witness-change-back := Invariant NoStartValueRewritten is violated.
EXPECTED_witness-from-pending-write := Invariant NoFileFromPendingWrite is violated.
EXPECTED_witness-late-accept := Invariant NoLateAccept is violated.
EXPECTED_plan-widen := Invariant NoLostEdit is violated.
EXPECTED_plan-diverge := Invariant Convergence is violated.
EXPECTED_plan-clip := Invariant NoLostEdit is violated.
EXPECTED_plan-delete-wins := Invariant NoLostEdit is violated.
EXPECTED_plan-single-splice := Invariant ShapeSingleSplice is violated.
EXPECTED_plan-file-prefix-suffix := Invariant OrderPreserved is violated.
EXPECTED_plan-file-prefix-suffix-lost := Invariant NoLostEdit is violated.
EXPECTED_plan-file-while-busy := Invariant NoDuplicate is violated.
EXPECTED_plan-remote-set-wins := Invariant Convergence is violated.

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
	cp $(MODEL) $(WORK)/translate/AgentEditorCoediting.tla
	@cd $(WORK)/translate && $(JAVA) -cp $(TOOLS) pcal.trans -nocfg AgentEditorCoediting.tla > $(WORK)/translate.log 2>&1
	@cat $(WORK)/translate.log
	cp $(WORK)/translate/AgentEditorCoediting.tla $(MODEL)

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
