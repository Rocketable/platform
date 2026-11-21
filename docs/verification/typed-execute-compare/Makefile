SHELL := /bin/bash
ROOT := $(abspath ../../..)
WORK := $(ROOT)/.tmp/run/typed-execute-compare
PROOF := $(CURDIR)/CompareRewrite.lean
LOG := $(WORK)/lean.log
STANDARD_AXIOMS := \[((propext|Classical\.choice|Quot\.sound)(, )?)+\]$$

LEAN_VERSION := 4.36.0
LEAN_ARCH := $(if $(filter aarch64 arm64,$(shell uname -m)),linux_aarch64,linux)
LEAN_SHA256_linux := 2b7ab1ed8b92d2e6abfaf41344823b510064dbc9c0260cac73eb496ab2dea6eb
LEAN_SHA256_linux_aarch64 := 35471998152191ef809418ce5bf61f8fc5f209e5e3be05b48bdc4949fdb3e726
LEAN_DIR := $(ROOT)/.tmp/lean
LEAN_ARCHIVE := $(LEAN_DIR)/lean-$(LEAN_VERSION)-$(LEAN_ARCH).tar.zst
LEAN := $(LEAN_DIR)/lean-$(LEAN_VERSION)-$(LEAN_ARCH)/bin/lean

.PHONY: all lean
all: lean

$(LEAN_ARCHIVE):
	mkdir -p $(LEAN_DIR)
	curl -fsSL -o $@.part https://github.com/leanprover/lean4/releases/download/v$(LEAN_VERSION)/lean-$(LEAN_VERSION)-$(LEAN_ARCH).tar.zst
	echo '$(LEAN_SHA256_$(LEAN_ARCH))  $@.part' | shasum -a 256 -c -
	mv $@.part $@

$(LEAN): $(LEAN_ARCHIVE)
	tar --zstd -xf $< -C $(LEAN_DIR)
	touch $@

lean: $(LEAN)
	@mkdir -p $(WORK)
	@$(LEAN) $(PROOF) > $(LOG) 2>&1; status=$$?; cat $(LOG); test $$status -eq 0
	@! grep -nE 'error|warning|sorry' $(LOG)
	@! grep -n native_decide $(PROOF)
	@test "$$(grep -c 'depends on axioms' $(LOG))" -eq "$$(grep -c '^#print axioms' $(PROOF))"
	@! grep 'depends on axioms' $(LOG) | grep -vE '$(STANDARD_AXIOMS)'
	@echo "lean: CompareRewrite.lean checked, every printed theorem uses only standard axioms"
