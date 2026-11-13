# Checks the Agent Pause model with TLC. AgentPause.tla holds the PlusCal algorithm, its properties and the generated
# translation; each .cfg is one check, run on its own.
#
#   make                  translate, then run every check
#   make -k               run every check even after one fails, to see them all
#   make goal             run one check
#   make translate        only translate the algorithm into the TLA+ in AgentPause.tla
#
# Needs curl, shasum and Java 11 or newer. The TLA+ tools, the translator's copy, Java's temporary files, TLC's
# metadata and every log live under the repository root's .tmp/run/agent-pause-model, never beside the model.
SHELL := /bin/bash
ROOT := $(abspath ../../..)
WORK := $(ROOT)/.tmp/run/agent-pause-model
MODEL := $(CURDIR)/AgentPause.tla
TLA_VERSION := 1.8.0
TLA_SHA256 := e1f11b75c4b5bc4478ba0ac5cb6ae28e40a01b0016bc33e0aa97da22dfdbcd7f
TOOLS := $(WORK)/tla2tools-$(TLA_VERSION).jar
JAVA := java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
# Every TLC run is killed after LIMIT seconds (SIGALRM; exit 142), which fails its check.
LIMIT := 600
PASS_WORKERS ?= 4
TLC := perl -e 'alarm shift; exec @ARGV' $(LIMIT) $(JAVA) -Xmx3g -cp $(TOOLS) tlc2.TLC -noGenerateSpecTE

# Required: the code as built, with its fixes, must keep every property these name.
PASS := safety callers queue sources goal goal-once stops
# Each must find the named violation: the gate removed, or one fix turned off.
NEGATIVE := no-gate no-gate-stops no-resubmit stranded duplicate repause-orphan orphan-baseline no-take
EXPECTED_no-take := Temporal property CallerReleasedWhenLive was violated.
EXPECTED_no-gate := Invariant ForwardLookingPause is violated.
EXPECTED_no-gate-stops := Temporal property StopsTurning was violated.
EXPECTED_no-resubmit := Temporal property GoalCompletes was violated.
EXPECTED_stranded := Temporal properties GoalCompletes and HeldRunsAfterResume were violated.
EXPECTED_duplicate := Invariant AtMostOneContinuation is violated.
EXPECTED_repause-orphan := Invariant CompletionOwned is violated.
EXPECTED_orphan-baseline := Invariant CompletionOwned is violated.

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
	cp $(MODEL) $(WORK)/translate/AgentPause.tla
	@cd $(WORK)/translate && $(JAVA) -cp $(TOOLS) pcal.trans -nocfg AgentPause.tla > $(WORK)/translate.log 2>&1; \
	status=$$?; cat $(WORK)/translate.log; test $$status -eq 0
	cp $(WORK)/translate/AgentPause.tla $(MODEL)

# A pass explores the whole graph; it uses PASS_WORKERS (4) workers.
$(PASS): translate
	@mkdir -p $(WORK)/$@
	@cd $(WORK) && $(TLC) -workers $(PASS_WORKERS) -metadir $(WORK)/$@ -config $(CURDIR)/$@.cfg $(MODEL) > $(WORK)/$@.log 2>&1; status=$$?; \
	cat $(WORK)/$@.log; test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

# A parser or tool failure is not the expected counterexample: require TLC's exit status for a violation (12 safety,
# 13 liveness) and the named violation, and keep the full trace in the log. One worker keeps the first
# counterexample, and so the trace, the same on every run.
$(NEGATIVE): translate
	@mkdir -p $(WORK)/$@
	@cd $(WORK) && $(TLC) -workers 1 -metadir $(WORK)/$@ -config $(CURDIR)/$@.cfg $(MODEL) > $(WORK)/$@.log 2>&1; status=$$?; \
	test $$status -eq 12 -o $$status -eq 13 && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|states generated|distinct states|depth of' $(WORK)/$@.log; echo '$@: expected counterexample confirmed'
