TLA_VERSION = 1.8.0
TLA_SHA256 = 67f9575c3b9ef7a0787b1bded34812862ffe79d711298e5a75b1a77720b19928
WORK ?= ../../../.tmp/verification/turn-resume-call-state
TOOLS = $(WORK)/tla2tools-$(TLA_VERSION).jar
SHA256SUM = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
JAVA = java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC = $(JAVA) -cp $(TOOLS) tlc2.TLC -noGenerateSpecTE -metadir $(WORK)/$@

PASS = fixed fixed-legacy fixed-idle-stop current-holds strict-record-first
FAIL = pre-fix aborted-no-state background-completed current idle-stop strict-current strict-stop strict-cancel \
	witness-failed witness-background witness-aborted-twice witness-rerun witness-reused-job witness-legacy \
	witness-user-stop
EXPECTED_pre-fix = Invariant KeepsFinishedState is violated
EXPECTED_aborted-no-state = Invariant KeepsFinishedState is violated
EXPECTED_background-completed = Invariant KeepsFinishedState is violated
EXPECTED_current = Invariant KeepsFinishedState is violated
EXPECTED_idle-stop = Invariant JournaledNeverRunning is violated
EXPECTED_strict-current = Invariant VisibleFinalKept is violated
EXPECTED_strict-stop = Invariant VisibleFinalKept is violated
EXPECTED_strict-cancel = Invariant VisibleFinalKept is violated
EXPECTED_witness-failed = Invariant NeverReplayedFailed is violated
EXPECTED_witness-background = Invariant NeverReplayedBackground is violated
EXPECTED_witness-aborted-twice = Invariant NeverStoppedReplayedTwice is violated
EXPECTED_witness-rerun = Invariant NeverRerun is violated
EXPECTED_witness-reused-job = Invariant NeverReusedJobRow is violated
EXPECTED_witness-legacy = Invariant NeverLegacyFailedResumed is violated
EXPECTED_witness-user-stop = Invariant NeverUserStopped is violated

.NOTPARALLEL:
.PHONY: all translate $(PASS) $(FAIL)

all: $(PASS) $(FAIL)

translate: $(TOOLS)
	mkdir -p $(WORK)/translate $(WORK)/java
	cp TurnResumeCallState.tla $(WORK)/translate/TurnResumeCallState.tla
	$(JAVA) -cp $(TOOLS) pcal.trans -nocfg $(WORK)/translate/TurnResumeCallState.tla
	cp $(WORK)/translate/TurnResumeCallState.tla TurnResumeCallState.tla

$(PASS): translate
	@$(TLC) -workers 1 -config $@.cfg TurnResumeCallState.tla > $(WORK)/$@.log 2>&1; status=$$?; cat $(WORK)/$@.log; \
	test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

$(FAIL): translate
	@$(TLC) -workers 1 -config $@.cfg TurnResumeCallState.tla > $(WORK)/$@.log 2>&1; status=$$?; \
	{ test $$status -eq 12 || test $$status -eq 13; } && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|states generated|distinct states found|depth of' $(WORK)/$@.log; echo '$@: expected counterexample confirmed'

$(TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	[ "$$($(SHA256SUM) $@ | cut -d ' ' -f 1)" = $(TLA_SHA256) ] || { echo "$@ does not match its checksum"; rm -f $@; exit 1; }
