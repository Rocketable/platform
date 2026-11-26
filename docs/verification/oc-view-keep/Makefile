# Checks the model of the page's one OpenCode view, kept mounted and hidden behind other tabs, with the TLC model
# checker. The model is the PlusCal algorithm in ViewKeep.tla; each .cfg file sets its switches for one check.
#
#   make                  translate the algorithm, then run every check
#   make keep             run one check
#   make translate        only translate the algorithm into the TLA+ in ViewKeep.tla
#
# Needs curl and Java 11 or newer on PATH. The TLA+ tools, the translator's copy, Java's temporary files, TLC's working
# files and each check's log go to WORK, the repository's .tmp/verification/oc-view-keep unless set otherwise.

TLA_VERSION = 1.8.0
TLA_SHA256 = 67f9575c3b9ef7a0787b1bded34812862ffe79d711298e5a75b1a77720b19928
WORK ?= ../../../.tmp/verification/oc-view-keep
TOOLS = $(WORK)/tla2tools-$(TLA_VERSION).jar
SHA256SUM = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
JAVA = java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC = $(JAVA) -cp $(TOOLS) tlc2.TLC -noGenerateSpecTE -metadir $(WORK)/$@

# The design must keep every property. Each other check must find the property it names violated: the shapes the
# design replaced, and each fence it needs.
PASS = keep keep-live
INVARIANT_FAIL = no-mark-gate no-slot-gate no-guard
TEMPORAL_FAIL = dispose-hidden
EXPECTED_no-mark-gate = Invariant MarkedWhileVisible is violated
EXPECTED_no-slot-gate = Invariant SlotOnlyShown is violated
EXPECTED_no-guard = Invariant NoOrphanView is violated
EXPECTED_dispose-hidden = Temporal property EventuallyWarm was violated

.PHONY: all translate $(PASS) $(INVARIANT_FAIL) $(TEMPORAL_FAIL)

all: $(PASS) $(INVARIANT_FAIL) $(TEMPORAL_FAIL)

# The translator leaves a .old backup beside the file it translates, so it translates a copy in WORK.
translate: $(TOOLS)
	mkdir -p $(WORK)/translate $(WORK)/java
	cp ViewKeep.tla $(WORK)/translate/ViewKeep.tla
	$(JAVA) -cp $(TOOLS) pcal.trans -nocfg $(WORK)/translate/ViewKeep.tla
	cp $(WORK)/translate/ViewKeep.tla ViewKeep.tla

$(PASS): translate
	@$(TLC) -workers auto -config $@.cfg ViewKeep.tla > $(WORK)/$@.log 2>&1; status=$$?; cat $(WORK)/$@.log; \
	test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

# A parse or tool error is not the expected counterexample: TLC must exit 12 (safety) or 13 (liveness) and name the
# violation. One worker makes the first counterexample the same on every run.
$(INVARIANT_FAIL): translate
	@$(TLC) -workers 1 -config $@.cfg ViewKeep.tla > $(WORK)/$@.log 2>&1; status=$$?; \
	test $$status -eq 12 && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|distinct states found' $(WORK)/$@.log; echo '$@: expected counterexample confirmed'

$(TEMPORAL_FAIL): translate
	@$(TLC) -workers 1 -config $@.cfg ViewKeep.tla > $(WORK)/$@.log 2>&1; status=$$?; \
	test $$status -eq 13 && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|distinct states found' $(WORK)/$@.log; echo '$@: expected counterexample confirmed'

$(TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	[ "$$($(SHA256SUM) $@ | cut -d ' ' -f 1)" = $(TLA_SHA256) ] || { echo "$@ does not match its checksum"; rm -f $@; exit 1; }
