SHELL := /bin/bash
ROOT := $(abspath ../../..)
WORK := $(ROOT)/.tmp/run/turn-secrets-model
MODEL := $(CURDIR)/TurnSecrets.tla
TLA_VERSION := 1.8.0
TLA_SHA256 := fdffef247f563ef6edbfd44958d2a82db79e7564e07cb673262d896353fa6713
TOOLS := $(WORK)/tla2tools-$(TLA_VERSION).jar
JAVA := java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC := $(JAVA) -Xmx1g -cp $(TOOLS) tlc2.TLC -workers 1 -noGenerateSpecTE
PASS := safe-native safe-opencode
NEGATIVE := erase-at-run-end no-inner-redaction no-replaced-redaction erase-without-run-reset
EXPECTED_erase-at-run-end := Invariant JournalNeverHoldsValue is violated.
EXPECTED_no-inner-redaction := Invariant JournalNeverHoldsValue is violated.
EXPECTED_no-replaced-redaction := Invariant JournalNeverHoldsValue is violated.
EXPECTED_erase-without-run-reset := Invariant NoValueCrossesTurns is violated.

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

translate: tools
	@mkdir -p $(WORK)/translate
	cp $(MODEL) $(WORK)/translate/TurnSecrets.tla
	@cd $(WORK)/translate && $(JAVA) -cp $(TOOLS) pcal.trans -nocfg TurnSecrets.tla > $(WORK)/translate.log 2>&1; status=$$?; \
	cat $(WORK)/translate.log; test $$status -eq 0
	cp $(WORK)/translate/TurnSecrets.tla $(MODEL)

$(PASS): translate
	@mkdir -p $(WORK)/$@
	@cd $(WORK) && $(TLC) -metadir $(WORK)/$@ -config $(CURDIR)/$@.cfg $(MODEL) > $(WORK)/$@.log 2>&1; status=$$?; \
	cat $(WORK)/$@.log; test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

$(NEGATIVE): translate
	@mkdir -p $(WORK)/$@
	@cd $(WORK) && $(TLC) -metadir $(WORK)/$@ -config $(CURDIR)/$@.cfg $(MODEL) > $(WORK)/$@.log 2>&1; status=$$?; \
	test $$status -eq 12 -o $$status -eq 13 && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|states generated|distinct states|depth of' $(WORK)/$@.log; echo '$@: expected counterexample confirmed'
