TLA_VERSION = 1.8.0
TLA_SHA256 = fdffef247f563ef6edbfd44958d2a82db79e7564e07cb673262d896353fa6713
WORK ?= ../../../.tmp/verification/background-job-finish
TOOLS = $(WORK)/tla2tools-$(TLA_VERSION).jar
SHA256SUM = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
JAVA = java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC = $(JAVA) -cp $(TOOLS) tlc2.TLC -noGenerateSpecTE -metadir $(WORK)/$@

PASS = fixed
FAIL = give-up-after-three block-shutdown skip-clean-abandoned save-cancelled wake-while-stopping witness-saved-after-refusals witness-gave-up-on-shutdown
EXPECTED_give-up-after-three = Temporal property SavedOnceDbWorks was violated
EXPECTED_block-shutdown = Temporal property ShutdownNotBlocked was violated
EXPECTED_skip-clean-abandoned = Invariant FinishedNeverKilled is violated
EXPECTED_save-cancelled = Invariant CancelledNeverCompleted is violated
EXPECTED_wake-while-stopping = Invariant NoWakeWhileStopping is violated
EXPECTED_witness-saved-after-refusals = Invariant NeverSavedAfterRefusals is violated
EXPECTED_witness-gave-up-on-shutdown = Invariant NeverGaveUpOnShutdown is violated

.NOTPARALLEL:
.PHONY: all translate $(PASS) $(FAIL)

all: $(PASS) $(FAIL)

translate: $(TOOLS)
	mkdir -p $(WORK)/translate $(WORK)/java
	cp BackgroundJobFinish.tla $(WORK)/translate/BackgroundJobFinish.tla
	$(JAVA) -cp $(TOOLS) pcal.trans -nocfg $(WORK)/translate/BackgroundJobFinish.tla
	cp $(WORK)/translate/BackgroundJobFinish.tla BackgroundJobFinish.tla

$(PASS): translate
	@$(TLC) -workers 1 -config $@.cfg BackgroundJobFinish.tla > $(WORK)/$@.log 2>&1; status=$$?; cat $(WORK)/$@.log; \
	test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

$(FAIL): translate
	@$(TLC) -workers 1 -config $@.cfg BackgroundJobFinish.tla > $(WORK)/$@.log 2>&1; status=$$?; \
	{ test $$status -eq 12 || test $$status -eq 13; } && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|states generated|distinct states found|depth of' $(WORK)/$@.log; echo '$@: expected counterexample confirmed'

$(TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	[ "$$($(SHA256SUM) $@ | cut -d ' ' -f 1)" = $(TLA_SHA256) ] || { echo "$@ does not match its checksum"; rm -f $@; exit 1; }
