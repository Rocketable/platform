TLA_VERSION = 1.8.0
TLA_SHA256 = fdffef247f563ef6edbfd44958d2a82db79e7564e07cb673262d896353fa6713
WORK ?= ../../../.tmp/verification/typed-execute-fanout
TOOLS = $(WORK)/tla2tools-$(TLA_VERSION).jar
SHA256SUM = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
JAVA = java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC = $(JAVA) -cp $(TOOLS) tlc2.TLC -noGenerateSpecTE -metadir $(WORK)/$@

PASS = gather try race race-first nested hang-turn hang-try hang-race hang-nested \
	code-gather code-try code-race code-race-first code-nested code-hang-turn code-hang-try code-hang-nested \
	code-cancel-denied code-cancel-error code-call-after-cancel
FAIL = cancel-recorded-denied cancel-recorded-error call-after-cancel try-lowest-index try-no-cancel try-no-cancel-hang \
	gather-crash-stops race-any nested-hold-slot witness-cancel-in-review witness-cancel-while-running \
	witness-try-not-lowest witness-gather-crash witness-race-loss-then-win witness-race-all-lose witness-slot-hand-off witness-hang
EXPECTED_cancel-recorded-denied = Invariant CancelledCallsRecordedCancelled is violated
EXPECTED_cancel-recorded-error = Invariant CancelledCallsRecordedCancelled is violated
EXPECTED_call-after-cancel = Invariant NoCallStartsAfterCancel is violated
EXPECTED_try-lowest-index = Invariant TryReturnsFirstFailure is violated
EXPECTED_try-no-cancel = Invariant NoBranchStartsAfterDecision is violated
EXPECTED_try-no-cancel-hang = Temporal property DecisionStops was violated
EXPECTED_gather-crash-stops = Invariant GatherCompletes is violated
EXPECTED_race-any = Invariant RaceNoFailureWhileSuccess is violated
EXPECTED_nested-hold-slot = Temporal property Terminates was violated
EXPECTED_witness-cancel-in-review = Invariant NeverCancelledInReview is violated
EXPECTED_witness-cancel-while-running = Invariant NeverCancelledWhileRunning is violated
EXPECTED_witness-try-not-lowest = Invariant NeverFirstAboveLowest is violated
EXPECTED_witness-gather-crash = Invariant NeverGatherCrashInList is violated
EXPECTED_witness-race-loss-then-win = Invariant NeverRaceLossThenWin is violated
EXPECTED_witness-race-all-lose = Invariant NeverRaceAllLose is violated
EXPECTED_witness-slot-hand-off = Invariant NeverSlotHandOff is violated
EXPECTED_witness-hang = Temporal property Terminates was violated

.NOTPARALLEL:
.PHONY: all translate $(PASS) $(FAIL)

all: $(PASS) $(FAIL)

translate: $(TOOLS)
	mkdir -p $(WORK)/translate $(WORK)/java
	cp FanOut.tla $(WORK)/translate/FanOut.tla
	$(JAVA) -cp $(TOOLS) pcal.trans -nocfg $(WORK)/translate/FanOut.tla
	cp $(WORK)/translate/FanOut.tla FanOut.tla

$(PASS): translate
	@$(TLC) -workers 1 -config $@.cfg FanOut.tla > $(WORK)/$@.log 2>&1; status=$$?; cat $(WORK)/$@.log; \
	test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

$(FAIL): translate
	@$(TLC) -workers 1 -config $@.cfg FanOut.tla > $(WORK)/$@.log 2>&1; status=$$?; \
	{ test $$status -eq 12 || test $$status -eq 13; } && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|states generated|distinct states found|depth of' $(WORK)/$@.log; echo '$@: expected counterexample confirmed'

$(TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	[ "$$($(SHA256SUM) $@ | cut -d ' ' -f 1)" = $(TLA_SHA256) ] || { echo "$@ does not match its checksum"; rm -f $@; exit 1; }
