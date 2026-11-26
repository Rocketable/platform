TLA_VERSION = 1.8.0
TLA_SHA256 = 67f9575c3b9ef7a0787b1bded34812862ffe79d711298e5a75b1a77720b19928
WORK ?= ../../../.tmp/verification/tailnet-web-gate
TOOLS = $(WORK)/tla2tools-$(TLA_VERSION).jar
SHA256SUM = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
JAVA = java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC = $(JAVA) -cp $(TOOLS) tlc2.TLC -noGenerateSpecTE -metadir $(WORK)/$@

PASS = fixed
FAIL = current-lock current-under current-returns current-lasting gatefirst-lock gatefirst-under fixed-waits fixed-restarts fixed-returns fixed-stops fixed-refused
EXPECTED_current-lock = Invariant LockFirst is violated
EXPECTED_current-under = Invariant NoMigrateUnderOther is violated
EXPECTED_current-returns = Temporal property WebComesBack was violated
EXPECTED_current-lasting = Temporal property LastingStops was violated
EXPECTED_gatefirst-lock = Invariant LockFirst is violated
EXPECTED_gatefirst-under = Invariant NoMigrateUnderOther is violated
EXPECTED_fixed-waits = Invariant NeverWaits is violated
EXPECTED_fixed-restarts = Invariant NeverRestarted is violated
EXPECTED_fixed-returns = Invariant NeverServesAfterRestart is violated
EXPECTED_fixed-stops = Invariant NeverGateFailure is violated
EXPECTED_fixed-refused = Invariant NeverRestartRefused is violated

.NOTPARALLEL:
.PHONY: all translate $(PASS) $(FAIL)

all: $(PASS) $(FAIL)

translate: $(TOOLS)
	mkdir -p $(WORK)/translate $(WORK)/java
	cp TailnetWebGate.tla $(WORK)/translate/TailnetWebGate.tla
	$(JAVA) -cp $(TOOLS) pcal.trans -nocfg $(WORK)/translate/TailnetWebGate.tla
	cp $(WORK)/translate/TailnetWebGate.tla TailnetWebGate.tla

$(PASS): translate
	@$(TLC) -workers 1 -config $@.cfg TailnetWebGate.tla > $(WORK)/$@.log 2>&1; status=$$?; cat $(WORK)/$@.log; \
	test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

$(FAIL): translate
	@$(TLC) -workers 1 -config $@.cfg TailnetWebGate.tla > $(WORK)/$@.log 2>&1; status=$$?; \
	{ test $$status -eq 12 || test $$status -eq 13; } && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|states generated|distinct states found|depth of' $(WORK)/$@.log; echo '$@: expected counterexample confirmed'

$(TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	[ "$$($(SHA256SUM) $@ | cut -d ' ' -f 1)" = $(TLA_SHA256) ] || { echo "$@ does not match its checksum"; rm -f $@; exit 1; }
