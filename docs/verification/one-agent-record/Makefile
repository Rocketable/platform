TLA_VERSION = 1.8.0
TLA_SHA256 = 67f9575c3b9ef7a0787b1bded34812862ffe79d711298e5a75b1a77720b19928
WORK ?= ../../../.tmp/verification/one-agent-record
TOOLS = $(WORK)/tla2tools-$(TLA_VERSION).jar
SHA256SUM = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
JAVA = java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC = $(JAVA) -cp $(TOOLS) tlc2.TLC -workers auto -noGenerateSpecTE -metadir $(WORK)/$@

PASSES = new-rule

old-rule-as-new_VIOLATION = Invariant SudoHermetic is violated
no-agreed-change_VIOLATION = Invariant OnlyAgreedDifferences is violated
implicit-self_VIOLATION = Invariant SelfOnlyWhenNamed is violated
task-ignores-sticky_VIOLATION = Invariant StickyNotTasked is violated
clone-unchecked_VIOLATION = Invariant CloneNeverWidens is violated
reads-unnamed_VIOLATION = Invariant NamedOnly is violated
sudo-reads-closed_VIOLATION = Invariant SudoReadsEverything is violated
keeps-receivers_VIOLATION = Invariant ReceiverListsIgnored is violated
sudo-ignores-denial_VIOLATION = Invariant SudoDenialHolds is violated
FAILS = old-rule-as-new no-agreed-change implicit-self task-ignores-sticky clone-unchecked reads-unnamed sudo-reads-closed keeps-receivers sudo-ignores-denial

.PHONY: all $(PASSES) $(FAILS)

all: $(PASSES) $(FAILS)

$(PASSES): $(TOOLS)
	@mkdir -p $(WORK)/java
	@$(TLC) -config $@.cfg DelegationReport.tla > $(WORK)/$@.log 2>&1; status=$$?; cat $(WORK)/$@.log; \
	test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

$(FAILS): $(TOOLS)
	@mkdir -p $(WORK)/java
	@$(TLC) -config $@.cfg Delegation.tla > $(WORK)/$@.log 2>&1; status=$$?; cat $(WORK)/$@.log; \
	{ test $$status -eq 12 || test $$status -eq 13; } && grep -q '$($@_VIOLATION)' $(WORK)/$@.log

$(TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	[ "$$($(SHA256SUM) $@ | cut -d ' ' -f 1)" = $(TLA_SHA256) ] || { echo "$@ does not match its checksum"; rm -f $@; exit 1; }
