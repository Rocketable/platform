TLA_VERSION = 1.8.0
TLA_SHA256 = 67f9575c3b9ef7a0787b1bded34812862ffe79d711298e5a75b1a77720b19928
WORK ?= ../../../.tmp/verification/upload-streaming
TOOLS = $(WORK)/tla2tools-$(TLA_VERSION).jar
SHA256SUM = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
JAVA = java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC
TLC = $(JAVA) -cp $(TOOLS) tlc2.TLC -noGenerateSpecTE -metadir $(WORK)/$@

PASS = fixed
FAIL = put-error-kept canceled-discard database-down witness-committed witness-queued witness-abort
EXPECTED_put-error-kept = Invariant NothingLeftBehind is violated
EXPECTED_canceled-discard = Invariant NothingLeftBehind is violated
EXPECTED_database-down = Invariant NothingLeftBehind is violated
EXPECTED_witness-committed = Invariant NeverCommitted is violated
EXPECTED_witness-queued = Invariant NeverQueued is violated
EXPECTED_witness-abort = Invariant NeverRemovedAfterAbort is violated

.NOTPARALLEL:
.PHONY: all translate $(PASS) $(FAIL)

all: $(PASS) $(FAIL)

translate: $(TOOLS)
	mkdir -p $(WORK)/translate $(WORK)/java
	cp UploadStreaming.tla $(WORK)/translate/UploadStreaming.tla
	$(JAVA) -cp $(TOOLS) pcal.trans -nocfg $(WORK)/translate/UploadStreaming.tla
	cp $(WORK)/translate/UploadStreaming.tla UploadStreaming.tla

$(PASS): translate
	@$(TLC) -workers 1 -config $@.cfg UploadStreaming.tla > $(WORK)/$@.log 2>&1; status=$$?; cat $(WORK)/$@.log; \
	test $$status -eq 0 && grep -q 'Model checking completed. No error has been found.' $(WORK)/$@.log

$(FAIL): translate
	@$(TLC) -workers 1 -config $@.cfg UploadStreaming.tla > $(WORK)/$@.log 2>&1; status=$$?; \
	{ test $$status -eq 12 || test $$status -eq 13; } && grep -Fq 'Error: $(EXPECTED_$@)' $(WORK)/$@.log || { cat $(WORK)/$@.log; exit 1; }; \
	grep -E 'Error:|states generated|distinct states found|depth of' $(WORK)/$@.log; echo '$@: expected counterexample confirmed'

$(TOOLS):
	mkdir -p $(dir $@)
	curl -fsSL -o $@ https://github.com/tlaplus/tlaplus/releases/download/v$(TLA_VERSION)/tla2tools.jar
	[ "$$($(SHA256SUM) $@ | cut -d ' ' -f 1)" = $(TLA_SHA256) ] || { echo "$@ does not match its checksum"; rm -f $@; exit 1; }
