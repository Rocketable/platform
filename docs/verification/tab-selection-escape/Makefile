SHELL := /bin/bash
ROOT := $(abspath ../../..)
WORK := $(ROOT)/.tmp/run/tab-selection-escape-model
MODEL := $(CURDIR)/TabSelectionEscape.tla
TLA_VERSION := 1.8.0
TLA_SHA256 := e1f11b75c4b5bc4478ba0ac5cb6ae28e40a01b0016bc33e0aa97da22dfdbcd7f
TOOLS := $(WORK)/tla2tools-$(TLA_VERSION).jar
JAVA := java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC := $(JAVA) -Xmx1g -cp $(TOOLS) tlc2.TLC -workers 1 -noGenerateSpecTE
PASS := fixed intended
NEGATIVE := old old-escape witness-race witness-clear
EXPECTED_old := Invariant GroupGetsSelection is violated.
EXPECTED_old-escape := Invariant EscapeWithMenuKeepsSelection is violated.
EXPECTED_witness-race := Invariant NoGroupAfterRace is violated.
EXPECTED_witness-clear := Invariant NoEscapeClearsOnPurpose is violated.

LEAN_VERSION := 4.36.0
LEAN_ARCH := $(if $(filter aarch64 arm64,$(shell uname -m)),linux_aarch64,linux)
LEAN_SHA256_linux := 2b7ab1ed8b92d2e6abfaf41344823b510064dbc9c0260cac73eb496ab2dea6eb
LEAN_SHA256_linux_aarch64 := 35471998152191ef809418ce5bf61f8fc5f209e5e3be05b48bdc4949fdb3e726
LEAN_DIR := $(ROOT)/.tmp/lean
LEAN_ARCHIVE := $(LEAN_DIR)/lean-$(LEAN_VERSION)-$(LEAN_ARCH).tar.zst
LEAN := $(LEAN_DIR)/lean-$(LEAN_VERSION)-$(LEAN_ARCH)/bin/lean

.NOTPARALLEL:
.PHONY: all check translate tools lean $(PASS) $(NEGATIVE)
all: check
check: $(PASS) $(NEGATIVE) lean

$(LEAN_ARCHIVE):
	mkdir -p $(LEAN_DIR)
	curl -fsSL -o $@.part https://github.com/leanprover/lean4/releases/download/v$(LEAN_VERSION)/lean-$(LEAN_VERSION)-$(LEAN_ARCH).tar.zst
	echo '$(LEAN_SHA256_$(LEAN_ARCH))  $@.part' | shasum -a 256 -c -
	mv $@.part $@

$(LEAN): $(LEAN_ARCHIVE)
	tar --zstd -xf $< -C $(LEAN_DIR)
	touch $@

lean: $(LEAN)
	$(LEAN) $(CURDIR)/TabSelectionEscape.lean
	@echo "lean: TabSelectionEscape.lean checked"

tools: $(TOOLS)
	@mkdir -p $(WORK)/java
	@echo '$(TLA_SHA256)  $(TOOLS)' | shasum -a 256 -c -

$(TOOLS):
	mkdir -p $(WORK)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar

translate: tools
	@mkdir -p $(WORK)/translate
	cp $(MODEL) $(WORK)/translate/TabSelectionEscape.tla
	@cd $(WORK)/translate && $(JAVA) -cp $(TOOLS) pcal.trans -nocfg TabSelectionEscape.tla > $(WORK)/translate.log 2>&1; \
	status=$$?; cat $(WORK)/translate.log; test $$status -eq 0
	cp $(WORK)/translate/TabSelectionEscape.tla $(MODEL)

$(PASS): translate
	@mkdir -p $(WORK)/$@
	@cd $(WORK) && $(TLC) -metadir $(WORK)/$@ -config $(CURDIR)/$@.cfg $(MODEL) > $(WORK)/$@.log 2>&1; status=$$?; \
	cat $(WORK)/$@.log; test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

$(NEGATIVE): translate
	@mkdir -p $(WORK)/$@
	@cd $(WORK) && $(TLC) -metadir $(WORK)/$@ -config $(CURDIR)/$@.cfg $(MODEL) > $(WORK)/$@.log 2>&1; status=$$?; \
	test $$status -eq 12 -o $$status -eq 13 && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	cat $(WORK)/$@.log; echo "$@: exit $$status, expected counterexample confirmed"
