# Checks the model of who else is typing in a Web chat with the TLC model checker. The model is the PlusCal algorithm
# in Typing.tla; each .cfg file sets its switches for one check, run on its own.
#
#   make                  translate the algorithm, then run every check (under a minute)
#   make built            run one check
#   make translate        only translate the algorithm into the TLA+ in Typing.tla
#
# Needs curl and Java 11 or newer on PATH. The TLA+ tools, the translator's copy, Java's temporary files, TLC's working
# files and each check's log go to WORK, the repository's .tmp/verification/typing unless set otherwise.

TLA_VERSION = 1.8.0
TLA_SHA256 = e1f11b75c4b5bc4478ba0ac5cb6ae28e40a01b0016bc33e0aa97da22dfdbcd7f
WORK ?= ../../../.tmp/verification/typing
TOOLS = $(WORK)/tla2tools-$(TLA_VERSION).jar
SHA256SUM = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
JAVA = java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC = $(JAVA) -cp $(TOOLS) tlc2.TLC -noGenerateSpecTE -metadir $(WORK)/$@

# The design as built must keep every requirement its .cfg names. Each other check must find the violation named here:
# a safety one (TLC exit 12) for an invariant, a liveness one (TLC exit 13) for a temporal property.
PASS = built
FAIL = unordered no-expiry unkeyed no-self-filter unheard seen overtaken stale dropped
EXPECTED_unordered = Invariant NoStaleReturn is violated
EXPECTED_no-expiry = Temporal property PillClears was violated
EXPECTED_unkeyed = Invariant NoCrossChat is violated
EXPECTED_no-self-filter = Invariant NoSelf is violated
EXPECTED_unheard = Invariant NoStaleAnywhere is violated
EXPECTED_seen = Invariant NeverSeen is violated
EXPECTED_overtaken = Invariant NeverOvertaken is violated
EXPECTED_stale = Invariant NeverStale is violated
EXPECTED_dropped = Invariant NeverDropped is violated

.PHONY: all translate $(PASS) $(FAIL)

all: $(PASS) $(FAIL)

# The translator leaves a .old backup beside the file it translates, so it translates a copy in WORK.
translate: $(TOOLS)
	mkdir -p $(WORK)/translate $(WORK)/java
	cp Typing.tla $(WORK)/translate/Typing.tla
	$(JAVA) -cp $(TOOLS) pcal.trans -nocfg $(WORK)/translate/Typing.tla
	cp $(WORK)/translate/Typing.tla Typing.tla

$(PASS): translate
	@$(TLC) -workers auto -config $@.cfg Typing.tla > $(WORK)/$@.log 2>&1; status=$$?; cat $(WORK)/$@.log; \
	test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

# A parse or tool error is not the expected counterexample: TLC must exit 12 or 13 and name the violation. One worker
# makes the first counterexample, and so the violation named, the same on every run.
$(FAIL): translate
	@$(TLC) -workers 1 -config $@.cfg Typing.tla > $(WORK)/$@.log 2>&1; status=$$?; \
	{ test $$status -eq 12 || test $$status -eq 13; } && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|distinct states found' $(WORK)/$@.log; echo '$@: expected counterexample confirmed'

$(TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	[ "$$($(SHA256SUM) $@ | cut -d ' ' -f 1)" = $(TLA_SHA256) ] || { echo "$@ does not match its checksum"; rm -f $@; exit 1; }
