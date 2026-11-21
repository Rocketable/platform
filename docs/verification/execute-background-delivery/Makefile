TLA_VERSION = 1.8.0
TLA_SHA256 = fdffef247f563ef6edbfd44958d2a82db79e7564e07cb673262d896353fa6713
WORK ?= ../../../.tmp/verification/execute-background-delivery
WORKERS ?= 4
TOOLS = $(WORK)/tla2tools-$(TLA_VERSION).jar
SHA256SUM = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
JAVA = java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC = $(JAVA) -cp $(TOOLS) tlc2.TLC -noGenerateSpecTE -metadir $(WORK)/$@

PASS = spec-one-job spec-two-jobs as-built-one-job as-built-two-jobs as-built-wake-merge as-built-late-move as-built-strict-phrase
FAIL = wake-merge late-move-listed phrase-by-store \
	separate-note-render duplicate-after-restart early-delete early-delete-fetch \
	witness-moved-note witness-late-move-foreground witness-killed-note witness-wake-delivery \
	witness-running-turn-delivery witness-later-turn-fetch witness-expired-fetch witness-degraded-note \
	witness-aborted-call witness-foreground-id-ends
EXPECTED_wake-merge = Invariant NoteCarriesOwnFiles is violated
EXPECTED_late-move-listed = Invariant MovedNoticeHonest is violated
EXPECTED_phrase-by-store = Invariant SameText is violated
EXPECTED_separate-note-render = Invariant SameText is violated
EXPECTED_duplicate-after-restart = Invariant ExactlyOnce is violated
EXPECTED_early-delete = Invariant RetainedDeletedOnlyBySweep is violated
EXPECTED_early-delete-fetch = Invariant HeldFetchableUntilExpiry is violated
EXPECTED_witness-moved-note = Invariant NeverMovedNote is violated
EXPECTED_witness-late-move-foreground = Invariant NeverLateMoveForeground is violated
EXPECTED_witness-killed-note = Invariant NeverKilledNote is violated
EXPECTED_witness-wake-delivery = Invariant NeverWakeDelivery is violated
EXPECTED_witness-running-turn-delivery = Invariant NeverDrainDelivery is violated
EXPECTED_witness-later-turn-fetch = Invariant NeverLaterTurnFetch is violated
EXPECTED_witness-expired-fetch = Invariant NeverExpiredFetch is violated
EXPECTED_witness-degraded-note = Invariant NeverDegradedNote is violated
EXPECTED_witness-aborted-call = Invariant NeverAbortedCall is violated
EXPECTED_witness-foreground-id-ends = Invariant NeverForegroundExpiredID is violated

.NOTPARALLEL:
.PHONY: all translate $(PASS) $(FAIL)

all: $(PASS) $(FAIL)

translate: $(TOOLS)
	mkdir -p $(WORK)/translate $(WORK)/java
	cp ExecuteBackgroundDelivery.tla $(WORK)/translate/ExecuteBackgroundDelivery.tla
	$(JAVA) -cp $(TOOLS) pcal.trans -nocfg $(WORK)/translate/ExecuteBackgroundDelivery.tla
	cp $(WORK)/translate/ExecuteBackgroundDelivery.tla ExecuteBackgroundDelivery.tla

$(PASS): translate
	@$(TLC) -workers $(WORKERS) -config $@.cfg ExecuteBackgroundDelivery.tla > $(WORK)/$@.log 2>&1; status=$$?; cat $(WORK)/$@.log; \
	test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

$(FAIL): translate
	@$(TLC) -workers 1 -config $@.cfg ExecuteBackgroundDelivery.tla > $(WORK)/$@.log 2>&1; status=$$?; \
	{ test $$status -eq 12 || test $$status -eq 13; } && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|states generated|distinct states found|depth of' $(WORK)/$@.log; echo '$@: expected counterexample confirmed'

$(TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	[ "$$($(SHA256SUM) $@ | cut -d ' ' -f 1)" = $(TLA_SHA256) ] || { echo "$@ does not match its checksum"; rm -f $@; exit 1; }
