TLA_VERSION = 1.8.0
TLA_SHA256 = 67f9575c3b9ef7a0787b1bded34812862ffe79d711298e5a75b1a77720b19928
WORK ?= ../../../.tmp/verification/oc-tab-recovery
TOOLS = $(WORK)/tla2tools-$(TLA_VERSION).jar
SHA256SUM = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
JAVA = java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC = $(JAVA) -cp $(TOOLS) tlc2.TLC -noGenerateSpecTE -metadir $(WORK)/$@

PASS = design without-identity before-empty
TEMPORAL_FAIL = before-blank no-retry no-timeline no-metadata hidden-server-error
EXPECTED_before-blank = Temporal property NoStuckBlank was violated
EXPECTED_no-retry = Temporal property Recovers was violated
EXPECTED_no-timeline = Temporal property Recovers was violated
EXPECTED_no-metadata = Temporal property Recovers was violated
EXPECTED_hidden-server-error = Temporal property ServerErrorVisible was violated

.PHONY: all translate $(PASS) $(TEMPORAL_FAIL)

all: $(PASS) $(TEMPORAL_FAIL)

translate: $(TOOLS)
	mkdir -p $(WORK)/translate $(WORK)/java
	cp OcTabRecovery.tla $(WORK)/translate/OcTabRecovery.tla
	$(JAVA) -cp $(TOOLS) pcal.trans -nocfg $(WORK)/translate/OcTabRecovery.tla
	cp $(WORK)/translate/OcTabRecovery.tla OcTabRecovery.tla

$(PASS): translate
	@$(TLC) -workers auto -config $@.cfg OcTabRecovery.tla > $(WORK)/$@.log 2>&1; status=$$?; cat $(WORK)/$@.log; \
	test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

$(TEMPORAL_FAIL): translate
	@$(TLC) -workers 1 -config $@.cfg OcTabRecovery.tla > $(WORK)/$@.log 2>&1; status=$$?; \
	test $$status -eq 13 && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|distinct states found' $(WORK)/$@.log; echo '$@: expected counterexample confirmed'

$(TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	[ "$$($(SHA256SUM) $@ | cut -d ' ' -f 1)" = $(TLA_SHA256) ] || { echo "$@ does not match its checksum"; rm -f $@; exit 1; }
