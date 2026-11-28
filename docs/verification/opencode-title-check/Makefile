TLA_VERSION = 1.8.0
TLA_SHA256 = 67f9575c3b9ef7a0787b1bded34812862ffe79d711298e5a75b1a77720b19928
WORK ?= ../../../.tmp/verification/opencode-title-check
TOOLS = $(WORK)/tla2tools-$(TLA_VERSION).jar
SHA256SUM = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
JAVA = java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC = $(JAVA) -cp $(TOOLS) tlc2.TLC -noGenerateSpecTE -metadir $(WORK)/$@

PASS = fixed fixed-wide fixed-commit-fail fixed-legacy fixed-legacy-commit-fail
FAIL = built stamp-first guard-off sync-adopts no-typed-marks unlocked-push drop-pending witness-retitled witness-kept witness-race witness-refused witness-mark
EXPECTED_built = Invariant ReplacesOnlyJudged is violated
EXPECTED_stamp-first = Action property NameNeverOlder is violated
EXPECTED_guard-off = Invariant ChecksNeverTakeTyped is violated
EXPECTED_sync-adopts = Invariant TypedNotGenerated is violated
EXPECTED_no-typed-marks = Invariant TypedNotGenerated is violated
EXPECTED_unlocked-push = Action property LatestTypedWins is violated
EXPECTED_drop-pending = Action property NameNeverOlder is violated
EXPECTED_witness-retitled = Invariant NeverRetitled is violated
EXPECTED_witness-kept = Invariant NeverKeptLocal is violated
EXPECTED_witness-race = Invariant NeverRacedAgainstCheck is violated
EXPECTED_witness-refused = Invariant NeverRefusedPush is violated
EXPECTED_witness-mark = Invariant NeverMarkSaved is violated

.NOTPARALLEL:
.PHONY: all translate $(PASS) $(FAIL)

all: $(PASS) $(FAIL)

translate: $(TOOLS)
	mkdir -p $(WORK)/translate $(WORK)/java
	cp OpenCodeTitleCheck.tla $(WORK)/translate/OpenCodeTitleCheck.tla
	$(JAVA) -cp $(TOOLS) pcal.trans -nocfg $(WORK)/translate/OpenCodeTitleCheck.tla
	cp $(WORK)/translate/OpenCodeTitleCheck.tla OpenCodeTitleCheck.tla

$(PASS): translate
	@$(TLC) -workers 1 -config $@.cfg OpenCodeTitleCheck.tla > $(WORK)/$@.log 2>&1; status=$$?; cat $(WORK)/$@.log; \
	test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

$(FAIL): translate
	@$(TLC) -workers 1 -config $@.cfg OpenCodeTitleCheck.tla > $(WORK)/$@.log 2>&1; status=$$?; \
	{ test $$status -eq 12 || test $$status -eq 13; } && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|states generated|distinct states found|depth of' $(WORK)/$@.log; echo '$@: expected counterexample confirmed'

$(TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	[ "$$($(SHA256SUM) $@ | cut -d ' ' -f 1)" = $(TLA_SHA256) ] || { echo "$@ does not match its checksum"; rm -f $@; exit 1; }
