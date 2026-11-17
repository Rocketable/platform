SHELL := /bin/bash
ROOT := $(abspath ../../..)
WORK := $(ROOT)/.tmp/permission-input-evidence-model
TLA_VERSION := 1.8.0
TLA_SHA256 := e1f11b75c4b5bc4478ba0ac5cb6ae28e40a01b0016bc33e0aa97da22dfdbcd7f
ARTIFACT_SHA256 := 03f5e519664174580692dad948e461c03a3fa0db0633bc0ab4f3471dd6310b4d
TOOLS := $(WORK)/tla2tools-$(TLA_VERSION).jar
JAVA_BIN ?= java
JAVA := $(JAVA_BIN) -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC := $(JAVA) -Xmx512m -cp $(TOOLS) tlc2.TLC -workers 1 -noGenerateSpecTE
NEGATIVE := compact-loss restart-loss aliased-snapshot turn-isolation
EXPECTED_compact-loss := Invariant OrderedEvidence is violated.
EXPECTED_restart-loss := Invariant OrderedEvidence is violated.
EXPECTED_aliased-snapshot := Invariant SnapshotStable is violated.
EXPECTED_turn-isolation := Invariant TurnIsolation is violated.

.NOTPARALLEL:
.PHONY: check tools translate pass $(NEGATIVE)
check: pass $(NEGATIVE)

$(TOOLS):
	mkdir -p $(WORK)
	gh api repos/tlaplus/vscode-tlaplus/actions/artifacts/13545024984/zip > $(WORK)/tools.zip
	echo '$(ARTIFACT_SHA256)  $(WORK)/tools.zip' | shasum -a 256 -c -
	unzip -p $(WORK)/tools.zip vscode-tlaplus-2026.11.62237.vsix > $(WORK)/tools.vsix
	unzip -p $(WORK)/tools.vsix extension/tools/tla2tools.jar > $@

tools: $(TOOLS)
	@mkdir -p $(WORK)/java
	@echo '$(TLA_SHA256)  $(TOOLS)' | shasum -a 256 -c -

translate: tools
	mkdir -p $(WORK)/translate
	cp InputEvidence.tla $(WORK)/translate/InputEvidence.tla
	cd $(WORK)/translate && $(JAVA) -cp $(TOOLS) pcal.trans -nocfg InputEvidence.tla > $(WORK)/translate.log 2>&1
	cat $(WORK)/translate.log
	cp $(WORK)/translate/InputEvidence.tla InputEvidence.tla

pass: translate
	@mkdir -p $(WORK)/pass
	@cd $(WORK) && $(TLC) -metadir $(WORK)/pass -config $(CURDIR)/pass.cfg $(CURDIR)/InputEvidence.tla > $(WORK)/pass.log 2>&1; status=$$?; \
	cat $(WORK)/pass.log; test $$status -eq 0 && grep -Fq 'Model checking completed. No error has been found.' $(WORK)/pass.log

$(NEGATIVE): translate
	@mkdir -p $(WORK)/$@
	@cd $(WORK) && $(TLC) -metadir $(WORK)/$@ -config $(CURDIR)/$@.cfg $(CURDIR)/InputEvidence.tla > $(WORK)/$@.log 2>&1; status=$$?; \
	test $$status -eq 12 -o $$status -eq 13 && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|states generated|distinct states|depth of|Finished in' $(WORK)/$@.log
