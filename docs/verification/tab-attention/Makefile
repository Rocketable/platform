# Checks the tab-attention model with the TLC model checker. The model is the PlusCal algorithm in TabAttention.tla;
# each .cfg file sets its switches for one check, run on its own.
#
#   make                  translate the algorithm, then run every check (about 1 minute)
#   make one-window       run one check
#   make translate        only translate the algorithm into the TLA+ in TabAttention.tla
#
# Needs curl and Java 11 or newer on PATH. The TLA+ tools and TLC's working files go to WORK, .tmp in this folder unless
# set otherwise.

TLA_VERSION = 1.8.0
TLA_SHA256 = e1f11b75c4b5bc4478ba0ac5cb6ae28e40a01b0016bc33e0aa97da22dfdbcd7f
WORK ?= .tmp
TOOLS = $(WORK)/tla2tools-$(TLA_VERSION).jar
SHA256SUM = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
TLC = java -XX:+UseParallelGC -cp $(TOOLS) tlc2.TLC -workers auto -metadir $(WORK)/$@

# The design as built, checked in two small worlds, as both at once would take too long: one browser window and a tab
# the person closes and opens again, and two windows with the tab open. Each must keep every requirement, and reach
# every state its _POSSIBLE line names.
CHECKS = one-window two-windows

.PHONY: all translate $(CHECKS)

all: $(CHECKS)

translate: $(TOOLS)
	java -cp $(TOOLS) pcal.trans -nocfg TabAttention.tla
	rm -f TabAttention.old

$(CHECKS): translate
	$(TLC) -config $@.cfg TabAttention.tla

$(TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	[ "$$($(SHA256SUM) $@ | cut -d ' ' -f 1)" = $(TLA_SHA256) ] || { echo "$@ does not match its checksum"; rm -f $@; exit 1; }
