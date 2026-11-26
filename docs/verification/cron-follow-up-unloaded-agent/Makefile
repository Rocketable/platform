# Checks the cron follow-up / unloaded agent model with TLC. CronFollowUpUnloadedAgent.tla holds the PlusCal
# algorithm, its properties and the generated translation; each .cfg is one check, run on its own.
#
#   make                  translate, then run every check
#   make design           run one check
#   make translate        only translate the algorithm into the TLA+ in CronFollowUpUnloadedAgent.tla
#
# Needs curl, shasum and Java 11 or newer. The TLA+ tools, the translator's copy, Java's temporary files, TLC's
# metadata and every log live under the repository root's .tmp/run/cron-follow-up-unloaded-agent.
SHELL := /bin/bash
ROOT := $(abspath ../../..)
WORK := $(ROOT)/.tmp/run/cron-follow-up-unloaded-agent
MODEL := $(CURDIR)/CronFollowUpUnloadedAgent.tla
TLA_VERSION := 1.8.0
TLA_SHA256 := 67f9575c3b9ef7a0787b1bded34812862ffe79d711298e5a75b1a77720b19928
TOOLS := $(WORK)/tla2tools-$(TLA_VERSION).jar
JAVA := java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC := $(JAVA) -Xmx4g -cp $(TOOLS) tlc2.TLC -workers auto -deadlock -noGenerateSpecTE

PASS := design design-wide drop-unrecorded chat-drops
NEGATIVE := design-strict drop-unrecorded-binds keep-stored split-drop drop-race agent-switch \
	witness-drop-then-deliver witness-failed-in-chat witness-drop-after-restart witness-drop-beside-unbound-chat
EXPECTED_design-strict := Temporal property DueDeliveredOrDropped was violated.
EXPECTED_drop-unrecorded-binds := Invariant NoBindOnUnloadedDecision is violated.
EXPECTED_keep-stored := Temporal property DueSettles was violated.
EXPECTED_split-drop := Invariant NeverDeliveredAndDropped is violated.
EXPECTED_drop-race := Invariant NoDropWhileLoadedAtCommit is violated.
EXPECTED_agent-switch := Invariant OwnAgentOnly is violated.
EXPECTED_witness-drop-then-deliver := Invariant NoDropThenDelivery is violated.
EXPECTED_witness-failed-in-chat := Invariant NoFailureInChat is violated.
EXPECTED_witness-drop-after-restart := Invariant NoDropAfterRestart is violated.
EXPECTED_witness-drop-beside-unbound-chat := Invariant NoDropBesideUnboundChat is violated.

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

translate: tools
	@mkdir -p $(WORK)/translate
	cp $(MODEL) $(WORK)/translate/CronFollowUpUnloadedAgent.tla
	@cd $(WORK)/translate && $(JAVA) -cp $(TOOLS) pcal.trans -nocfg CronFollowUpUnloadedAgent.tla > $(WORK)/translate.log 2>&1
	@cat $(WORK)/translate.log
	cp $(WORK)/translate/CronFollowUpUnloadedAgent.tla $(MODEL)

$(PASS): translate
	@mkdir -p $(WORK)/$@
	@cd $(WORK) && $(TLC) -metadir $(WORK)/$@ -config $(CURDIR)/$@.cfg $(MODEL) > $(WORK)/$@.log 2>&1; status=$$?; \
	cat $(WORK)/$@.log; test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

$(NEGATIVE): translate
	@mkdir -p $(WORK)/$@
	@cd $(WORK) && $(TLC) -metadir $(WORK)/$@ -config $(CURDIR)/$@.cfg $(MODEL) > $(WORK)/$@.log 2>&1; status=$$?; \
	test $$status -eq 12 -o $$status -eq 13 && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|states generated|distinct states|depth of' $(WORK)/$@.log; echo '$@: expected counterexample confirmed'
