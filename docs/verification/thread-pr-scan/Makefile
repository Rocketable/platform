# Checks the model of the incremental pull request scan of a remote OpenCode thread with the TLC model checker. The
# model is the PlusCal algorithm in ThreadPRScan.tla; each .cfg file sets its switches for one check.
#
#   make                  translate the algorithm, then run every check
#   make scan             run one check
#   make translate        only translate the algorithm into the TLA+ in ThreadPRScan.tla
#
# Needs curl and Java 11 or newer on PATH. The TLA+ tools, the translator's copy, Java's temporary files, TLC's working
# files and each check's log go to WORK, the repository's .tmp/verification/thread-pr-scan unless set otherwise.

TLA_VERSION = 1.8.0
TLA_SHA256 = fdffef247f563ef6edbfd44958d2a82db79e7564e07cb673262d896353fa6713
WORK ?= ../../../.tmp/verification/thread-pr-scan
TOOLS = $(WORK)/tla2tools-$(TLA_VERSION).jar
SHA256SUM = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)

LEAN_VERSION = 4.36.0
LEAN_ARCH = $(if $(filter aarch64 arm64,$(shell uname -m)),linux_aarch64,linux)
LEAN_SHA256_linux = 2b7ab1ed8b92d2e6abfaf41344823b510064dbc9c0260cac73eb496ab2dea6eb
LEAN_SHA256_linux_aarch64 = 35471998152191ef809418ce5bf61f8fc5f209e5e3be05b48bdc4949fdb3e726
LEAN_DIR = ../../../.tmp/lean
LEAN_ARCHIVE = $(LEAN_DIR)/lean-$(LEAN_VERSION)-$(LEAN_ARCH).tar.zst
LEAN = $(LEAN_DIR)/lean-$(LEAN_VERSION)-$(LEAN_ARCH)/bin/lean
JAVA = java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC = $(JAVA) -cp $(TOOLS) tlc2.TLC -noGenerateSpecTE -metadir $(WORK)/$@

# The design must keep its requirement. Each other check must find it violated without one of the design's rules.
PASS = scan
INVARIANT_FAIL = no-boundary-check past-unfinished changed-parents-only silent-spawn
EXPECTED_no-boundary-check = Invariant Agreement is violated
EXPECTED_past-unfinished = Invariant Agreement is violated
EXPECTED_changed-parents-only = Invariant Agreement is violated
EXPECTED_silent-spawn = Invariant Agreement is violated

.PHONY: all lean translate $(PASS) $(INVARIANT_FAIL)

all: lean $(PASS) $(INVARIANT_FAIL)

# The translator leaves a .old backup beside the file it translates, so it translates a copy in WORK.
translate: $(TOOLS)
	mkdir -p $(WORK)/translate $(WORK)/java
	cp ThreadPRScan.tla $(WORK)/translate/ThreadPRScan.tla
	$(JAVA) -cp $(TOOLS) pcal.trans -nocfg $(WORK)/translate/ThreadPRScan.tla
	cp $(WORK)/translate/ThreadPRScan.tla ThreadPRScan.tla

$(PASS): translate
	@$(TLC) -workers auto -config $@.cfg ThreadPRScan.tla > $(WORK)/$@.log 2>&1; status=$$?; cat $(WORK)/$@.log; \
	test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

# A parse or tool error is not the expected counterexample: TLC must exit 12 and name the violation. One worker makes the first counterexample the same on every run.
$(INVARIANT_FAIL): translate
	@$(TLC) -workers 1 -config $@.cfg ThreadPRScan.tla > $(WORK)/$@.log 2>&1; status=$$?; \
	test $$status -eq 12 && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|distinct states found' $(WORK)/$@.log; echo '$@: expected counterexample confirmed'

$(TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	[ "$$($(SHA256SUM) $@ | cut -d ' ' -f 1)" = $(TLA_SHA256) ] || { echo "$@ does not match its checksum"; rm -f $@; exit 1; }

lean: $(LEAN)
	$(LEAN) ThreadPRScan.lean
	@echo "lean: ThreadPRScan.lean checked"

$(LEAN_ARCHIVE):
	mkdir -p $(LEAN_DIR)
	curl -fsSL -o $@.part https://github.com/leanprover/lean4/releases/download/v$(LEAN_VERSION)/lean-$(LEAN_VERSION)-$(LEAN_ARCH).tar.zst
	[ "$$($(SHA256SUM) $@.part | cut -d ' ' -f 1)" = $(LEAN_SHA256_$(LEAN_ARCH)) ] || { echo "$@ does not match its checksum"; rm -f $@.part; exit 1; }
	mv $@.part $@

$(LEAN): $(LEAN_ARCHIVE)
	tar --zstd -xf $< -C $(LEAN_DIR)
	touch $@
