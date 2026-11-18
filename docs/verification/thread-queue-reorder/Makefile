TLA_VERSION = 1.8.0
TLA_SHA256 = e1f11b75c4b5bc4478ba0ac5cb6ae28e40a01b0016bc33e0aa97da22dfdbcd7f
WORK ?= ../../../.tmp/verification/thread-queue-reorder
TOOLS = $(WORK)/tla2tools-$(TLA_VERSION).jar
SHA256SUM = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
JAVA = java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC = $(JAVA) -cp $(TOOLS) tlc2.TLC -noGenerateSpecTE -metadir $(WORK)/$@

PASS = fixed
FAIL = current-collide current-half current-order witness-changed witness-pick witness-stale-list witness-failed dispatch-window
EXPECTED_current-collide = Invariant DistinctPositions is violated
EXPECTED_current-half = Invariant OrderMatches is violated
EXPECTED_current-order = Invariant WorkerFollowsOrder is violated
EXPECTED_witness-changed = Invariant NeverChanged is violated
EXPECTED_witness-pick = Invariant NeverPickedAfterChange is violated
EXPECTED_witness-stale-list = Invariant NeverStaleList is violated
EXPECTED_witness-failed = Invariant NeverFailed is violated
EXPECTED_dispatch-window = Invariant NoStaleClaim is violated

.NOTPARALLEL:
.PHONY: all translate $(PASS) $(FAIL)

all: $(PASS) $(FAIL)

translate: $(TOOLS)
	mkdir -p $(WORK)/translate $(WORK)/java
	cp ThreadQueueReorder.tla $(WORK)/translate/ThreadQueueReorder.tla
	$(JAVA) -cp $(TOOLS) pcal.trans -nocfg $(WORK)/translate/ThreadQueueReorder.tla
	cp $(WORK)/translate/ThreadQueueReorder.tla ThreadQueueReorder.tla

$(PASS): translate
	@$(TLC) -workers 1 -config $@.cfg ThreadQueueReorder.tla > $(WORK)/$@.log 2>&1; status=$$?; cat $(WORK)/$@.log; \
	test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

$(FAIL): translate
	@$(TLC) -workers 1 -config $@.cfg ThreadQueueReorder.tla > $(WORK)/$@.log 2>&1; status=$$?; \
	{ test $$status -eq 12 || test $$status -eq 13; } && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|states generated|distinct states found|depth of' $(WORK)/$@.log; echo '$@: expected counterexample confirmed'

$(TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	[ "$$($(SHA256SUM) $@ | cut -d ' ' -f 1)" = $(TLA_SHA256) ] || { echo "$@ does not match its checksum"; rm -f $@; exit 1; }
