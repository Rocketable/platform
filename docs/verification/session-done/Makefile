# Checks the session-done models with the TLC model checker. Doorbell.tla is the design as built: fast signals wake
# the checks RocketClaw already runs, and an OpenCode park waits for an idle marker newer than its own prompt.
# OrderedLog.tla is the rejected design that follows OpenCode's ordered session log instead.
#
#   make                  translate both algorithms, then run every check (about 20 minutes)
#   make a-recovery       run one check
#   make translate        only translate the algorithms into the TLA+ in their files
#
# Needs curl and Java 11 or newer on PATH. The TLA+ tools, the translator's copies, Java's temporary files, TLC's
# working files and each check's log go to WORK, the repository's .tmp/verification/session-done unless set otherwise.

TLA_VERSION = 1.8.0
TLA_SHA256 = fdffef247f563ef6edbfd44958d2a82db79e7564e07cb673262d896353fa6713
WORK ?= ../../../.tmp/verification/session-done
TOOLS = $(WORK)/tla2tools-$(TLA_VERSION).jar
SHA256SUM = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
JAVA = java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC = $(JAVA) -cp $(TOOLS) tlc2.TLC -workers auto -noGenerateSpecTE -metadir $(WORK)/$@

# a-bells-only and a-recovery are the design as built, with every signal delivered and with signals lost and the
# periodic checks running; each must keep every property. c-replay is the ordered-log design when OpenCode replays
# its log, and must pass too.
PASSES = a-bells-only a-recovery c-replay

# Each of these must find its named violation: TLC exits 12 or 13 and names it, so a parse error never counts as the
# expected failure. a-no-racefix is the design before this change, a-no-recheck the sync without its second read of
# OpenCode's active sessions, a-lossy-no-timers the design without its periodic checks, and c-no-replay the ordered
# log as `opencode serve` serves it.
a-no-racefix_VIOLATION = Invariant NoEarlyEnd is violated
a-no-recheck_VIOLATION = Invariant NoEarlyEnd is violated
a-lossy-no-timers_VIOLATION = Temporal property ScreenCatchesUp was violated
c-no-replay_VIOLATION = Temporal property ScreenCatchesUp was violated
FAILS = a-no-racefix a-no-recheck a-lossy-no-timers c-no-replay

.PHONY: all translate $(PASSES) $(FAILS)

all: $(PASSES) $(FAILS)

# The translator leaves a .old backup beside the file it translates, so it translates copies in WORK.
translate: $(TOOLS)
	mkdir -p $(WORK)/translate $(WORK)/java
	cp Doorbell.tla OrderedLog.tla $(WORK)/translate/
	$(JAVA) -cp $(TOOLS) pcal.trans -nocfg $(WORK)/translate/Doorbell.tla
	$(JAVA) -cp $(TOOLS) pcal.trans -nocfg $(WORK)/translate/OrderedLog.tla
	cp $(WORK)/translate/Doorbell.tla $(WORK)/translate/OrderedLog.tla .

module = $(if $(filter c-%,$1),OrderedLog,Doorbell)

$(PASSES): translate
	@$(TLC) -config $@.cfg $(call module,$@).tla > $(WORK)/$@.log 2>&1; status=$$?; cat $(WORK)/$@.log; \
	test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

$(FAILS): translate
	@$(TLC) -config $@.cfg $(call module,$@).tla > $(WORK)/$@.log 2>&1; status=$$?; cat $(WORK)/$@.log; \
	{ test $$status -eq 12 || test $$status -eq 13; } && grep -q '$($@_VIOLATION)' $(WORK)/$@.log

$(TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	[ "$$($(SHA256SUM) $@ | cut -d ' ' -f 1)" = $(TLA_SHA256) ] || { echo "$@ does not match its checksum"; rm -f $@; exit 1; }
