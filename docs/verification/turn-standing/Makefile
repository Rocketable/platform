# Checks the turn standing model with TLC. TurnStanding.tla holds the PlusCal algorithm, its properties and the
# generated translation; each .cfg is one check, run on its own.
#
#   make                  translate, then run every check
#   make -k               run every check even after one fails, to see them all
#   make safety           run one check
#   make translate        only translate the algorithm into the TLA+ in TurnStanding.tla
#
# Needs curl, shasum, perl and Java 11 or newer. The TLA+ tools, the translator's copy, Java's temporary files,
# TLC's metadata and every log live under the repository root's .tmp/run/turn-standing-model, never beside the model.
SHELL := /bin/bash
ROOT := $(abspath ../../..)
WORK := $(ROOT)/.tmp/run/turn-standing-model
MODEL := $(CURDIR)/TurnStanding.tla
TLA_VERSION := 1.8.0
TLA_SHA256 := 67f9575c3b9ef7a0787b1bded34812862ffe79d711298e5a75b1a77720b19928
TOOLS := $(WORK)/tla2tools-$(TLA_VERSION).jar
JAVA := java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
# Every TLC run is killed after LIMIT seconds (SIGALRM; exit 142), which fails its check.
LIMIT := 1800
PASS_WORKERS ?= 4
TLC := perl -e 'alarm shift; exec @ARGV' $(LIMIT) $(JAVA) -Xmx6g -cp $(TOOLS) tlc2.TLC -noGenerateSpecTE

# Required: the design must keep every property these name.
PASS := safety safety-inputs liveness
# Each must find the named violation: one rule turned off, the accepted non-guarantee, or a witness showing a path
# the passes cover is reachable.
NEGATIVE := mixed-wake steer-any drain-any non-waking-may-wait witness-join witness-slip witness-both-wakes \
	witness-late-note
EXPECTED_mixed-wake := Invariant NoMixedTurn is violated.
EXPECTED_steer-any := Invariant NoMixedTurn is violated.
EXPECTED_drain-any := Invariant NoMixedTurn is violated.
EXPECTED_non-waking-may-wait := Temporal property NonWakingNotesRun was violated.
EXPECTED_witness-join := Invariant NoSteerJoins is violated.
EXPECTED_witness-slip := Invariant NoSlipJoin is violated.
EXPECTED_witness-both-wakes := Invariant NoTwoStandingWakes is violated.
EXPECTED_witness-late-note := Invariant NoLateNote is violated.

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
	cp $(MODEL) $(WORK)/translate/TurnStanding.tla
	@cd $(WORK)/translate && $(JAVA) -cp $(TOOLS) pcal.trans -nocfg TurnStanding.tla > $(WORK)/translate.log 2>&1; \
	status=$$?; cat $(WORK)/translate.log; test $$status -eq 0
	cp $(WORK)/translate/TurnStanding.tla $(MODEL)

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
