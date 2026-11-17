SHELL := /bin/bash
WORK := $(abspath ../../../.tmp/draft-presence-model)
MODEL := $(CURDIR)/DraftPresence.tla
TOOLS := $(WORK)/tla2tools-1.8.0.jar
SHA := e1f11b75c4b5bc4478ba0ac5cb6ae28e40a01b0016bc33e0aa97da22dfdbcd7f
VSIX := https://open-vsx.org/api/tlaplus/vscode-ide/2026.11.62237/file/tlaplus.vscode-ide-2026.11.62237.vsix
JAVA := java -Djava.io.tmpdir=$(WORK)
TLC := $(JAVA) -cp $(TOOLS) tlc2.TLC -workers 1 -noGenerateSpecTE

.PHONY: check translate
check: translate
	cd $(WORK) && { $(TLC) -metadir serialized -config $(CURDIR)/serialized.cfg DraftPresence.tla > serialized.log 2>&1 || { cat serialized.log; exit 1; }; }
	grep -F 'Model checking completed. No error has been found.' $(WORK)/serialized.log
	cd $(WORK) && { $(TLC) -metadir overlap -config $(CURDIR)/overlap.cfg DraftPresence.tla > overlap.log 2>&1; status=$$?; \
		{ test $$status -eq 12 || test $$status -eq 13; } && grep -F 'Error: Invariant NoCycle is violated.' overlap.log || { cat overlap.log; exit 1; }; }

translate: $(TOOLS)
	echo '$(SHA)  $(TOOLS)' | shasum -a 256 -c -
	cp $(MODEL) $(WORK)/DraftPresence.tla
	cd $(WORK) && $(JAVA) -cp $(TOOLS) pcal.trans -nocfg DraftPresence.tla
	cp $(WORK)/DraftPresence.tla $(MODEL)

$(TOOLS):
	mkdir -p $(WORK)
	curl -fsSL $(VSIX) -o $(WORK)/extension.vsix
	unzip -p $(WORK)/extension.vsix extension/tools/tla2tools.jar > $@.part
	echo '$(SHA)  $@.part' | shasum -a 256 -c -
	mv $@.part $@
