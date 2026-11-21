TLA_VERSION = 1.8.0
TLA_SHA256 = 7e53aa636dac8a3cb0533f343115c0548e03fc0ce5edc7739becf58d3d0fb84f
WORK ?= ../../../.tmp/verification/typed-execute-emfile
TOOLS = $(WORK)/tla2tools-$(TLA_VERSION).jar
SHA256SUM = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
JAVA = java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC = $(JAVA) -cp $(TOOLS) tlc2.TLC -noGenerateSpecTE -metadir $(WORK)/$@

PASS = retry retry-two-step retry-cancel recycle-progress
FAIL = no-backoff keep-open keep-open-leak wake-only hold-dir unserialized cancel-race uncancellable-slot \
	recycle-starve witness-wait witness-wake witness-enfile witness-cancel
EXPECTED_no-backoff = Invariant NoSpin is violated
EXPECTED_keep-open = Temporal property AllReadsEnd was violated
EXPECTED_keep-open-leak = Invariant NoLeak is violated
EXPECTED_wake-only = Temporal property AllReadsEnd was violated
EXPECTED_hold-dir = Temporal property AllReadsEnd was violated
EXPECTED_unserialized = Temporal property AllReadsEnd was violated
EXPECTED_cancel-race = Invariant NoRetryAfterCancel is violated
EXPECTED_uncancellable-slot = Temporal property CancelEnds was violated
EXPECTED_recycle-starve = Temporal property EachReaderKeepsReading was violated
EXPECTED_witness-wait = Invariant NeverRetriedThenRead is violated
EXPECTED_witness-wake = Invariant NeverRetriedThenRead is violated
EXPECTED_witness-enfile = Invariant NeverEnfile is violated
EXPECTED_witness-cancel = Invariant NeverCancelled is violated

.NOTPARALLEL:
.PHONY: all translate $(PASS) $(FAIL)

all: $(PASS) $(FAIL)

translate: $(TOOLS)
	mkdir -p $(WORK)/translate $(WORK)/java
	cp ReadFdRetry.tla $(WORK)/translate/ReadFdRetry.tla
	$(JAVA) -cp $(TOOLS) pcal.trans -nocfg $(WORK)/translate/ReadFdRetry.tla
	cp $(WORK)/translate/ReadFdRetry.tla ReadFdRetry.tla

$(PASS): translate
	@$(TLC) -workers 1 -config $@.cfg ReadFdRetry.tla > $(WORK)/$@.log 2>&1; status=$$?; cat $(WORK)/$@.log; \
	test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

$(FAIL): translate
	@$(TLC) -workers 1 -config $@.cfg ReadFdRetry.tla > $(WORK)/$@.log 2>&1; status=$$?; \
	{ test $$status -eq 12 || test $$status -eq 13; } && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|states generated|distinct states found|depth of' $(WORK)/$@.log; echo '$@: expected counterexample confirmed'

$(TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	[ "$$($(SHA256SUM) $@ | cut -d ' ' -f 1)" = $(TLA_SHA256) ] || { echo "$@ does not match its checksum"; rm -f $@; exit 1; }
