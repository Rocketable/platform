# Checks the sticky: slack model with TLC. SlackSticky.tla holds the PlusCal algorithm, its properties and the
# generated translation; each .cfg is one check, run on its own.
#
#   make                  translate, then run every check
#   make -k               run every check even after one fails, to see them all
#   make safety           run one check
#   make translate        only translate the algorithm into the TLA+ in SlackSticky.tla
#
# Needs curl, shasum, perl and Java 11 or newer. The TLA+ tools, the translator's copy, Java's temporary files,
# TLC's metadata and every log live under the repository root's .tmp/run/slack-sticky-model, never beside the model.
SHELL := /bin/bash
ROOT := $(abspath ../../..)
WORK := $(ROOT)/.tmp/run/slack-sticky-model
MODEL := $(CURDIR)/SlackSticky.tla
TLA_VERSION := 1.8.0
TLA_SHA256 := fdffef247f563ef6edbfd44958d2a82db79e7564e07cb673262d896353fa6713
TOOLS := $(WORK)/tla2tools-$(TLA_VERSION).jar
JAVA := java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
# Every TLC run is killed after LIMIT seconds (SIGALRM; exit 142), which fails its check.
LIMIT := 1800
PASS_WORKERS ?= 4
TLC := perl -e 'alarm shift; exec @ARGV' $(LIMIT) $(JAVA) -Xmx6g -cp $(TOOLS) tlc2.TLC -noGenerateSpecTE

# Required: the design must keep every property these name.
PASS := safety sources stops config replies liveness-thread liveness-channel liveness-web liveness-agent liveness-config liveness-replies \
	ambient-thread ambient-watched ambient-config liveness-ambient-thread liveness-ambient-edit liveness-ambient-watched
# Each must find the named violation: one design rule turned off, one behavior the current code has kept, or a
# witness showing a path the passes cover is reachable.
NEGATIVE := no-ratchet volatile-ratchet split-move no-hold no-root-first per-conv-dedup thread-only-lookup \
	legacy-report no-start-check key-without-agent stop-by-conv settle-after-close no-footer-switch no-wake-on-release \
	witness-move-held witness-reply-thread witness-web-held witness-stop-moved witness-ignored short-window witness-expiry \
	ambient-mentions ambient-root-channel ambient-mention-root no-reply-footer volatile-no-reply deferred-session \
	witness-ambient-reply
EXPECTED_no-ratchet := Invariant RatchetHonored is violated.
EXPECTED_volatile-ratchet := Invariant RatchetHonored is violated.
EXPECTED_split-move := Temporal property ChannelKnowsThread was violated.
EXPECTED_no-hold := Invariant HeldUntilRootSettles is violated.
EXPECTED_no-root-first := Invariant HeldUntilRootSettles is violated.
EXPECTED_per-conv-dedup := Invariant OneAdmission is violated.
EXPECTED_thread-only-lookup := Invariant OneReply is violated.
EXPECTED_legacy-report := Temporal property MentionsAnswered was violated.
EXPECTED_no-start-check := Invariant OnlySlackAgentsRun is violated.
EXPECTED_key-without-agent := Invariant SessionAgentFixed is violated.
EXPECTED_stop-by-conv := Invariant StopPrecise is violated.
EXPECTED_settle-after-close := Temporal property StatusClears was violated.
EXPECTED_no-footer-switch := Invariant FooterMatchesSession is violated.
EXPECTED_no-wake-on-release := Temporal property Settles was violated.
EXPECTED_witness-move-held := Invariant NoMoveThenHeld is violated.
EXPECTED_witness-reply-thread := Invariant NoReplyThread is violated.
EXPECTED_witness-web-held := Invariant NoWebHeldAfterMove is violated.
EXPECTED_witness-stop-moved := Invariant NoStopAfterMove is violated.
EXPECTED_witness-ignored := Invariant NoIgnoredAtPick is violated.
EXPECTED_short-window := Invariant OneAdmission is violated.
EXPECTED_witness-expiry := Invariant NoExpiry is violated.
EXPECTED_ambient-mentions := Invariant MentionNeverAmbient is violated.
EXPECTED_ambient-root-channel := Invariant AmbientInThread is violated.
EXPECTED_ambient-mention-root := Invariant NoAmbientUnderMentionRoot is violated.
EXPECTED_no-reply-footer := Invariant SilentPostsNothing is violated.
EXPECTED_volatile-no-reply := Invariant SilentPostsNothing is violated.
EXPECTED_deferred-session := Invariant AmbientWritesNoStatus is violated.
EXPECTED_witness-ambient-reply := Invariant NoAmbientAnswer is violated.

.NOTPARALLEL:
.PHONY: all check translate tools $(PASS) $(NEGATIVE)
all: check
check: $(PASS) $(NEGATIVE)

tools: $(TOOLS)
	@mkdir -p $(WORK)/java
	@echo '$(TLA_SHA256)  $(TOOLS)' | shasum -a 256 -c -

$(TOOLS):
	mkdir -p $(WORK)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar

# Translate a scratch copy so pcal's .old backup never appears beside the model.
translate: tools
	@mkdir -p $(WORK)/translate
	cp $(MODEL) $(WORK)/translate/SlackSticky.tla
	@cd $(WORK)/translate && $(JAVA) -cp $(TOOLS) pcal.trans -nocfg SlackSticky.tla > $(WORK)/translate.log 2>&1; \
	status=$$?; cat $(WORK)/translate.log; test $$status -eq 0
	cp $(WORK)/translate/SlackSticky.tla $(MODEL)

# A pass explores the whole graph; it uses PASS_WORKERS (4) workers.
$(PASS): translate
	@mkdir -p $(WORK)/$@
	@cd $(WORK) && $(TLC) -workers $(PASS_WORKERS) -metadir $(WORK)/$@ -config $(CURDIR)/$@.cfg $(MODEL) > $(WORK)/$@.log 2>&1; status=$$?; \
	cat $(WORK)/$@.log; test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

# A parser or tool failure is not the expected counterexample: require TLC's exit status for a violation (12 safety,
# 13 liveness) and the named violation, and keep the full trace in the log. One worker keeps the first
# counterexample, and so the trace, the same on every run.
$(NEGATIVE): translate
	@mkdir -p $(WORK)/$@
	@cd $(WORK) && $(TLC) -workers 1 -metadir $(WORK)/$@ -config $(CURDIR)/$@.cfg $(MODEL) > $(WORK)/$@.log 2>&1; status=$$?; \
	test $$status -eq 12 -o $$status -eq 13 && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|states generated|distinct states|depth of' $(WORK)/$@.log; echo '$@: expected counterexample confirmed'
