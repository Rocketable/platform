# Checks the model of which session a remote OpenCode tab shows with the TLC model checker. The model is the PlusCal
# algorithm in ViewSession.tla; each .cfg file sets its switches for one check, run on its own.
#
#   make                  translate the algorithm, then run every check (a few minutes)
#   make view             run one check
#   make translate        only translate the algorithm into the TLA+ in ViewSession.tla
#
# Needs curl and Java 11 or newer on PATH. The TLA+ tools and TLC's working files go to WORK, the repository's .tmp
# unless set otherwise.

TLA_VERSION = 1.8.0
TLA_SHA256 = e1f11b75c4b5bc4478ba0ac5cb6ae28e40a01b0016bc33e0aa97da22dfdbcd7f
WORK ?= ../../../.tmp/verification
TOOLS = $(WORK)/tla2tools-$(TLA_VERSION).jar
SHA256SUM = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
TLC = java -XX:+UseParallelGC -cp $(TOOLS) tlc2.TLC -workers auto -noGenerateSpecTE -metadir $(WORK)/$@

# The design as built must keep every requirement and drop-request must pass. Each other check must find the
# invariant its first line names violated: the shapes the design replaced, and what the design must still allow.
PASS = view drop-request
FAIL = view-child view-request before stale-only open-only

.PHONY: all translate $(PASS) $(FAIL)

all: $(PASS) $(FAIL)

translate: $(TOOLS)
	java -cp $(TOOLS) pcal.trans -nocfg ViewSession.tla
	rm -f ViewSession.old

$(PASS): translate
	$(TLC) -config $@.cfg ViewSession.tla

$(FAIL): translate
	@mkdir -p $(WORK)
	@if $(TLC) -config $@.cfg ViewSession.tla > $(WORK)/$@.out; then echo "$@: expected a violation"; exit 1; fi
	@grep "is violated" $(WORK)/$@.out || { cat $(WORK)/$@.out; exit 1; }

$(TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	[ "$$($(SHA256SUM) $@ | cut -d ' ' -f 1)" = $(TLA_SHA256) ] || { echo "$@ does not match its checksum"; rm -f $@; exit 1; }
