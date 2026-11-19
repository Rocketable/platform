# Checks the tab-attention model with the TLC model checker. The model is the PlusCal algorithm in TabAttention.tla;
# each .cfg file sets its switches for one check, run on its own.
#
#   make                  translate the algorithm, then run every check (about 8 minutes)
#   make one-window       run one check
#   make translate        only translate the algorithm into the TLA+ in TabAttention.tla
#
# Needs curl and Java 11 or newer on PATH. The TLA+ tools, the translator's copy, Java's temporary files, TLC's working
# files and each check's log go to WORK, the repository's .tmp/verification/tab-attention unless set otherwise.

TLA_VERSION = 1.8.0
TLA_SHA256 = 7e53aa636dac8a3cb0533f343115c0548e03fc0ce5edc7739becf58d3d0fb84f
WORK ?= ../../../.tmp/verification/tab-attention
TOOLS = $(WORK)/tla2tools-$(TLA_VERSION).jar
SHA256SUM = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
JAVA = java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC = $(JAVA) -cp $(TOOLS) tlc2.TLC -workers auto -noGenerateSpecTE -metadir $(WORK)/$@

# The design as built, checked in two small worlds, as both at once would take too long: one browser window and a tab
# the person closes and opens again, and two windows with the tab open. Each must keep every requirement, and reach
# every state its _POSSIBLE line names. fail-old-turn-end runs the replaced rule and must find DoneEndsOnlyWhenIdle
# violated: TLC exits 12 or 13 and names it, so a parse error never counts as the expected failure.
CHECKS = one-window two-windows

.PHONY: all translate $(CHECKS) fail-old-turn-end

all: $(CHECKS) fail-old-turn-end

# The translator leaves a .old backup beside the file it translates, so it translates a copy in WORK.
translate: $(TOOLS)
	mkdir -p $(WORK)/translate $(WORK)/java
	cp TabAttention.tla $(WORK)/translate/TabAttention.tla
	$(JAVA) -cp $(TOOLS) pcal.trans -nocfg $(WORK)/translate/TabAttention.tla
	cp $(WORK)/translate/TabAttention.tla TabAttention.tla

$(CHECKS): translate
	@$(TLC) -config $@.cfg TabAttention.tla > $(WORK)/$@.log 2>&1; status=$$?; cat $(WORK)/$@.log; \
	test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

fail-old-turn-end: translate
	@$(TLC) -config old-turn-end.cfg TabAttention.tla > $(WORK)/old-turn-end.log 2>&1; status=$$?; cat $(WORK)/old-turn-end.log; \
	{ test $$status -eq 12 || test $$status -eq 13; } && grep -q 'Invariant DoneEndsOnlyWhenIdle is violated' $(WORK)/old-turn-end.log

$(TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	[ "$$($(SHA256SUM) $@ | cut -d ' ' -f 1)" = $(TLA_SHA256) ] || { echo "$@ does not match its checksum"; rm -f $@; exit 1; }
