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
TLA_SHA256 = e1f11b75c4b5bc4478ba0ac5cb6ae28e40a01b0016bc33e0aa97da22dfdbcd7f
WORK ?= ../../../.tmp/verification/oc-view-session
TOOLS = $(WORK)/tla2tools-$(TLA_VERSION).jar
SHA256SUM = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
JAVA = java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC = $(JAVA) -cp $(TOOLS) tlc2.TLC -noGenerateSpecTE -metadir $(WORK)/$@

# The design as built must keep every requirement and drop-request must pass. Each other check must find the
# invariant its first line names violated: the shapes the design replaced, and what the design must still allow.
PASS = view drop-request
FAIL = view-child view-request before stale-only open-only
EXPECTED_view-child = ChildNeverShown
EXPECTED_view-request = RequestNeverOpened
EXPECTED_before = Agreement
EXPECTED_stale-only = Agreement
EXPECTED_open-only = NoHijack

.PHONY: all translate $(PASS) $(FAIL)

all: $(PASS) $(FAIL)

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
