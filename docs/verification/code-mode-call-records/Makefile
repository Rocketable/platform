TLA_VERSION = 1.8.0
TLA_SHA256 = 67f9575c3b9ef7a0787b1bded34812862ffe79d711298e5a75b1a77720b19928
WORK ?= ../../../.tmp/verification/code-mode-call-records
TOOLS = $(WORK)/tla2tools-$(TLA_VERSION).jar
SHA256SUM = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
JAVA = java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC = $(JAVA) -cp $(TOOLS) tlc2.TLC -noGenerateSpecTE -metadir $(WORK)/$@

PASS = fixed fixed-three-calls kill-then-cancel-one-restart kill-then-cancel-holds
FAIL = kill-then-cancel pre-fix no-row-lock snapshot-wins no-recovery-cancel sloppy-prefix \
	witness-merged-late witness-picked-up witness-cancelled-saved witness-cancelled-scratch witness-finish-waits
EXPECTED_kill-then-cancel = Invariant JobEndsCalls is violated
EXPECTED_pre-fix = Invariant NoLoss is violated
EXPECTED_no-row-lock = Invariant NoLoss is violated
EXPECTED_snapshot-wins = Action property NeverOlder is violated
EXPECTED_no-recovery-cancel = Invariant JobEndsCalls is violated
EXPECTED_sloppy-prefix = Invariant OtherUntouched is violated
EXPECTED_witness-merged-late = Invariant NeverMergedLate is violated
EXPECTED_witness-picked-up = Invariant NeverPickedUp is violated
EXPECTED_witness-cancelled-saved = Invariant NeverCancelledSaved is violated
EXPECTED_witness-cancelled-scratch = Invariant NeverCancelledScratchSaved is violated
EXPECTED_witness-finish-waits = Invariant NeverFinishWaits is violated

.NOTPARALLEL:
.PHONY: all translate $(PASS) $(FAIL)

all: $(PASS) $(FAIL)

translate: $(TOOLS)
	mkdir -p $(WORK)/translate $(WORK)/java
	cp CodeModeCallRecords.tla $(WORK)/translate/CodeModeCallRecords.tla
	$(JAVA) -cp $(TOOLS) pcal.trans -nocfg $(WORK)/translate/CodeModeCallRecords.tla
	cp $(WORK)/translate/CodeModeCallRecords.tla CodeModeCallRecords.tla

$(PASS): translate
	@$(TLC) -workers 1 -config $@.cfg CodeModeCallRecords.tla > $(WORK)/$@.log 2>&1; status=$$?; cat $(WORK)/$@.log; \
	test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

$(FAIL): translate
	@$(TLC) -workers 1 -config $@.cfg CodeModeCallRecords.tla > $(WORK)/$@.log 2>&1; status=$$?; \
	{ test $$status -eq 12 || test $$status -eq 13; } && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|states generated|distinct states found|depth of' $(WORK)/$@.log; echo '$@: expected counterexample confirmed'

$(TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	[ "$$($(SHA256SUM) $@ | cut -d ' ' -f 1)" = $(TLA_SHA256) ] || { echo "$@ does not match its checksum"; rm -f $@; exit 1; }
