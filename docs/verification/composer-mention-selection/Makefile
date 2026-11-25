SHELL := /bin/bash
override WORK := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))../../..)/.tmp/composer-mention-selection
MODEL := ComposerMentionSelection.tla
TLA_VERSION := 1.8.0
TLA_SHA256 := fdffef247f563ef6edbfd44958d2a82db79e7564e07cb673262d896353fa6713
TOOLS := $(WORK)/tla2tools.jar
JAVA := java -Djava.io.tmpdir=$(WORK)/java -XX:-UsePerfData -XX:+UseParallelGC -cp $(TOOLS)
TLC := $(JAVA) -Xmx256m tlc2.TLC -workers 1 -noGenerateSpecTE

.PHONY: check translate fixed old
check: fixed old

$(TOOLS):
	mkdir -p $(WORK)
	curl -fsSL -o $@.part https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	echo '$(TLA_SHA256)  $@.part' | shasum -a 256 -c -
	mv $@.part $@

translate: $(TOOLS)
	echo '$(TLA_SHA256)  $(TOOLS)' | shasum -a 256 -c -
	mkdir -p $(WORK)/java
	cp $(MODEL) $(WORK)/$(MODEL)
	cd $(WORK) && $(JAVA) pcal.trans -nocfg $(MODEL) > translate.log 2>&1 || { cat $(WORK)/translate.log; exit 1; }
	cat $(WORK)/translate.log

fixed old: translate
	@cd $(WORK) && $(TLC) -metadir $(WORK)/$@ -config $(CURDIR)/$@.cfg $(MODEL) > $@.log 2>&1; status=$$?; \
	cat $@.log; \
	if [ "$@" = fixed ]; then \
		test $$status -eq 0 && grep -Fq 'Model checking completed. No error has been found.' $@.log; \
	else \
		{ test $$status -eq 12 || test $$status -eq 13; } && grep -Fxq 'Error: Invariant ReplacementRemovesTag is violated.' $@.log; \
	fi
