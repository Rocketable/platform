SHELL := /bin/bash
ROOT := $(abspath ../../..)
WORK := $(ROOT)/.tmp/run/composer-voice-readiness-model
MODEL := $(CURDIR)/ComposerVoiceReadiness.tla
TLA_VERSION := 1.8.0
TLA_SHA256 := 7e53aa636dac8a3cb0533f343115c0548e03fc0ce5edc7739becf58d3d0fb84f
VSIX := https://open-vsx.org/api/tlaplus/vscode-ide/2026.11.150034/file/tlaplus.vscode-ide-2026.11.150034.vsix
TOOLS := $(WORK)/tla2tools-$(TLA_VERSION).jar
JAVA := java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC := $(JAVA) -Xmx1g -cp $(TOOLS) tlc2.TLC -workers 1 -noGenerateSpecTE
PASS := fixed
NEGATIVE := old witness-preferred witness-settings-error witness-identity-error witness-agent-error witness-empty-catalog witness-hangup
EXPECTED_old := NoDefaultWhilePreferencePending
EXPECTED_witness-preferred := NoPreferredStartAfterBlockedClick
EXPECTED_witness-settings-error := NoStartAfterSettingsError
EXPECTED_witness-identity-error := NoBlockedClickAfterIdentityError
EXPECTED_witness-agent-error := NoStartAfterAgentError
EXPECTED_witness-empty-catalog := NoStartWithEmptyVoiceCatalog
EXPECTED_witness-hangup := NoHangupWhilePending

.NOTPARALLEL:
.PHONY: all check translate tools $(PASS) $(NEGATIVE)
all: check
check: $(PASS) $(NEGATIVE)

$(TOOLS):
	mkdir -p $(WORK)
	curl -fsSL -o $(WORK)/extension.vsix $(VSIX)
	unzip -p $(WORK)/extension.vsix extension/tools/tla2tools.jar > $@.part
	echo '$(TLA_SHA256)  $@.part' | shasum -a 256 -c -
	mv $@.part $@

tools: $(TOOLS)
	@mkdir -p $(WORK)/java
	@echo '$(TLA_SHA256)  $(TOOLS)' | shasum -a 256 -c -

translate: tools
	@mkdir -p $(WORK)/translate
	cp $(MODEL) $(WORK)/translate/ComposerVoiceReadiness.tla
	@cd $(WORK)/translate && $(JAVA) -cp $(TOOLS) pcal.trans -nocfg ComposerVoiceReadiness.tla > $(WORK)/translate.log 2>&1; \
	status=$$?; cat $(WORK)/translate.log; test $$status -eq 0
	cp $(WORK)/translate/ComposerVoiceReadiness.tla $(MODEL)

$(PASS): translate
	@cd $(WORK) && $(TLC) -metadir $(WORK)/$@ -config $(CURDIR)/$@.cfg $(MODEL) > $(WORK)/$@.log 2>&1; status=$$?; \
	cat $(WORK)/$@.log; test $$status -eq 0 && grep -Fq 'Model checking completed. No error has been found.' $(WORK)/$@.log

$(NEGATIVE): translate
	@cd $(WORK) && $(TLC) -metadir $(WORK)/$@ -config $(CURDIR)/$@.cfg $(MODEL) > $(WORK)/$@.log 2>&1; status=$$?; \
	test $$status -eq 12 -o $$status -eq 13 && grep -Fxq 'Error: Invariant $(EXPECTED_$@) is violated.' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	cat $(WORK)/$@.log; echo "$@: exit $$status, expected counterexample confirmed"
