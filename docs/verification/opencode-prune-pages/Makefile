# Checks the model of how RocketClaw forgets local copies of remote OpenCode sessions with the TLC model checker. The
# model is the PlusCal algorithm in OpenCodePrune.tla; each .cfg file sets its switches for one check, run on its own.
#
#   make                  translate the algorithm, then run every check (under a minute)
#   make fixed            run one check
#   make translate        only translate the algorithm into the TLA+ in OpenCodePrune.tla
#
# Needs curl and Java 11 or newer on PATH. The TLA+ tools, the translator's copy, Java's temporary files, TLC's working
# files and each check's log go to WORK, the repository's .tmp/verification/opencode-prune-pages unless set otherwise.

TLA_VERSION = 1.8.0
TLA_SHA256 = e1f11b75c4b5bc4478ba0ac5cb6ae28e40a01b0016bc33e0aa97da22dfdbcd7f
WORK ?= ../../../.tmp/verification/opencode-prune-pages
TOOLS = $(WORK)/tla2tools-$(TLA_VERSION).jar
SHA256SUM = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
JAVA = java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC = $(JAVA) -cp $(TOOLS) tlc2.TLC -noGenerateSpecTE -metadir $(WORK)/$@

# The design as built must keep every requirement its .cfg names. Each other check must find the violation named here:
# a safety one (TLC exit 12) for an invariant, a liveness one (TLC exit 13) for a temporal property.
PASS = fixed fixed-wide
FAIL = current paged-desc forget-on-failure fixed-slow fixed-forgets fixed-pages
EXPECTED_current = Temporal property PruneCompletes was violated
EXPECTED_paged-desc = Invariant NoLiveForgotten is violated
EXPECTED_forget-on-failure = Invariant NoLiveForgotten is violated
EXPECTED_fixed-slow = Temporal property PruneCompletes was violated
EXPECTED_fixed-forgets = Invariant NothingForgotten is violated
EXPECTED_fixed-pages = Invariant NeverMultiPage is violated

.NOTPARALLEL:
.PHONY: all translate $(PASS) $(FAIL)

all: $(PASS) $(FAIL)

translate: $(TOOLS)
	mkdir -p $(WORK)/translate $(WORK)/java
	cp OpenCodePrune.tla $(WORK)/translate/OpenCodePrune.tla
	$(JAVA) -cp $(TOOLS) pcal.trans -nocfg $(WORK)/translate/OpenCodePrune.tla
	cp $(WORK)/translate/OpenCodePrune.tla OpenCodePrune.tla

$(PASS): translate
	@$(TLC) -workers 1 -config $@.cfg OpenCodePrune.tla > $(WORK)/$@.log 2>&1; status=$$?; cat $(WORK)/$@.log; \
	test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

$(FAIL): translate
	@$(TLC) -workers 1 -config $@.cfg OpenCodePrune.tla > $(WORK)/$@.log 2>&1; status=$$?; \
	{ test $$status -eq 12 || test $$status -eq 13; } && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|states generated|distinct states found|depth of' $(WORK)/$@.log; echo '$@: expected counterexample confirmed'

$(TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	[ "$$($(SHA256SUM) $@ | cut -d ' ' -f 1)" = $(TLA_SHA256) ] || { echo "$@ does not match its checksum"; rm -f $@; exit 1; }
