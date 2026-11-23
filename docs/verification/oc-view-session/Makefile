# Checks the model of which session a remote OpenCode tab shows with the TLC model checker. The model is the PlusCal
# algorithm in ViewSession.tla; each .cfg file sets its switches for one check, run on its own.
#
#   make                  translate the algorithm, then run every check (under a minute)
#   make view             run one check
#   make translate        only translate the algorithm into the TLA+ in ViewSession.tla
#
# Needs curl and Java 11 or newer on PATH. The TLA+ tools, the translator's copy, Java's temporary files, TLC's working
# files and each check's log go to WORK, the repository's .tmp/verification/oc-view-session unless set otherwise.

TLA_VERSION = 1.8.0
TLA_SHA256 = fdffef247f563ef6edbfd44958d2a82db79e7564e07cb673262d896353fa6713
WORK ?= ../../../.tmp/verification/oc-view-session
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

# The design as built must keep every requirement and drop-request must pass. Each other check must find the
# invariant its first line names violated: the shapes the design replaced, and what the design must still allow.
PASS = view drop-request
FAIL = view-child view-request before stale-only open-only id-keys
EXPECTED_view-child = ChildNeverShown
EXPECTED_view-request = RequestNeverOpened
EXPECTED_before = Agreement
EXPECTED_stale-only = Agreement
EXPECTED_open-only = NoHijack
EXPECTED_id-keys = Agreement

.PHONY: all lean translate $(PASS) $(FAIL)

all: lean $(PASS) $(FAIL)

# The translator leaves a .old backup beside the file it translates, so it translates a copy in WORK.
translate: $(TOOLS)
	mkdir -p $(WORK)/translate $(WORK)/java
	cp ViewSession.tla $(WORK)/translate/ViewSession.tla
	$(JAVA) -cp $(TOOLS) pcal.trans -nocfg $(WORK)/translate/ViewSession.tla
	cp $(WORK)/translate/ViewSession.tla ViewSession.tla

$(PASS): translate
	@$(TLC) -workers auto -config $@.cfg ViewSession.tla > $(WORK)/$@.log 2>&1; status=$$?; cat $(WORK)/$@.log; \
	test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

# A parse or tool error is not the expected counterexample: TLC must exit 12 (safety violation) and name the invariant.
# One worker makes the first counterexample, and so the invariant named, the same on every run.
$(FAIL): translate
	@$(TLC) -workers 1 -config $@.cfg ViewSession.tla > $(WORK)/$@.log 2>&1; status=$$?; \
	test $$status -eq 12 && grep -Fq 'Error: Invariant $(EXPECTED_$@) is violated' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|distinct states found' $(WORK)/$@.log; echo '$@: expected counterexample confirmed'

$(TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	[ "$$($(SHA256SUM) $@ | cut -d ' ' -f 1)" = $(TLA_SHA256) ] || { echo "$@ does not match its checksum"; rm -f $@; exit 1; }

lean: $(LEAN)
	$(LEAN) SessionTabs.lean
	@echo "lean: SessionTabs.lean checked"

$(LEAN_ARCHIVE):
	mkdir -p $(LEAN_DIR)
	curl -fsSL -o $@.part https://github.com/leanprover/lean4/releases/download/v$(LEAN_VERSION)/lean-$(LEAN_VERSION)-$(LEAN_ARCH).tar.zst
	[ "$$($(SHA256SUM) $@.part | cut -d ' ' -f 1)" = $(LEAN_SHA256_$(LEAN_ARCH)) ] || { echo "$@ does not match its checksum"; rm -f $@.part; exit 1; }
	mv $@.part $@

$(LEAN): $(LEAN_ARCHIVE)
	tar --zstd -xf $< -C $(LEAN_DIR)
	touch $@
