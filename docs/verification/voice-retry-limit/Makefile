TLA_VERSION = 1.8.0
TLA_SHA256 = fdffef247f563ef6edbfd44958d2a82db79e7564e07cb673262d896353fa6713
WORK ?= ../../../.tmp/verification/voice-retry-limit
TOOLS = $(WORK)/tla2tools-$(TLA_VERSION).jar
SHA256SUM = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
JAVA = java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC = $(JAVA) -cp $(TOOLS) tlc2.TLC -noGenerateSpecTE -metadir $(WORK)/$@

PASS = fixed
FAIL = current-bound current-ends no-reset witness-reconnect witness-stop witness-reset
EXPECTED_current-bound = Invariant BoundedRetries is violated
EXPECTED_current-ends = Temporal property OutageEnds was violated
EXPECTED_no-reset = Invariant NoEarlyStop is violated
EXPECTED_witness-reconnect = Invariant NeverReconnectAfterFailures is violated
EXPECTED_witness-stop = Invariant NeverStopped is violated
EXPECTED_witness-reset = Invariant NeverStopAfterReconnect is violated

.NOTPARALLEL:
.PHONY: all translate $(PASS) $(FAIL)

all: $(PASS) $(FAIL)

translate: $(TOOLS)
	mkdir -p $(WORK)/translate $(WORK)/java
	cp VoiceRetryLimit.tla $(WORK)/translate/VoiceRetryLimit.tla
	$(JAVA) -cp $(TOOLS) pcal.trans -nocfg $(WORK)/translate/VoiceRetryLimit.tla
	cp $(WORK)/translate/VoiceRetryLimit.tla VoiceRetryLimit.tla

$(PASS): translate
	@$(TLC) -workers 1 -config $@.cfg VoiceRetryLimit.tla > $(WORK)/$@.log 2>&1; status=$$?; cat $(WORK)/$@.log; \
	test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

$(FAIL): translate
	@$(TLC) -workers 1 -config $@.cfg VoiceRetryLimit.tla > $(WORK)/$@.log 2>&1; status=$$?; \
	{ test $$status -eq 12 || test $$status -eq 13; } && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|states generated|distinct states found|depth of' $(WORK)/$@.log; echo '$@: expected counterexample confirmed'

$(TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	[ "$$($(SHA256SUM) $@ | cut -d ' ' -f 1)" = $(TLA_SHA256) ] || { echo "$@ does not match its checksum"; rm -f $@; exit 1; }
