# Checks the tab-attention model with the TLC model checker. The model is the PlusCal algorithm in TabAttention.tla;
# each .cfg file sets its switches for one check, run on its own.
#
#   make                  translate the algorithm, then run every check (about 1 minute)
#   make one-window       run one check
#   make translate        only translate the algorithm into the TLA+ in TabAttention.tla
#
# Needs curl and Java 11 or newer on PATH. The TLA+ tools, the translator's copy, Java's temporary files, TLC's working
# files and each check's log go to WORK, the repository's .tmp/verification/tab-attention unless set otherwise.

TLA_VERSION = 1.8.0
TLA_SHA256 = e1f11b75c4b5bc4478ba0ac5cb6ae28e40a01b0016bc33e0aa97da22dfdbcd7f
WORK ?= ../../../.tmp/verification/tab-attention
TOOLS = $(WORK)/tla2tools-$(TLA_VERSION).jar
SHA256SUM = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
JAVA = java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC = $(JAVA) -cp $(TOOLS) tlc2.TLC -workers auto -noGenerateSpecTE -metadir $(WORK)/$@

# The design as built, checked in two small worlds, as both at once would take too long: one browser window and a tab
# the person closes and opens again, and two windows with the tab open. Each must keep every requirement, and reach
# every state its _POSSIBLE line names.
CHECKS = one-window two-windows

.PHONY: all translate $(CHECKS)

all: $(CHECKS)

# The translator leaves a .old backup beside the file it translates, so it translates a copy in WORK.
translate: $(TOOLS)
	mkdir -p $(WORK)/translate $(WORK)/java
	cp TabAttention.tla $(WORK)/translate/TabAttention.tla
	$(JAVA) -cp $(TOOLS) pcal.trans -nocfg $(WORK)/translate/TabAttention.tla
	cp $(WORK)/translate/TabAttention.tla TabAttention.tla

$(CHECKS): translate
	@$(TLC) -config $@.cfg TabAttention.tla > $(WORK)/$@.log 2>&1; status=$$?; cat $(WORK)/$@.log; \
	test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

$(TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	[ "$$($(SHA256SUM) $@ | cut -d ' ' -f 1)" = $(TLA_SHA256) ] || { echo "$@ does not match its checksum"; rm -f $@; exit 1; }
