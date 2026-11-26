SHELL := /bin/bash
WORK := $(abspath ../../../.tmp/run/live-hosted-page-pane)
MODEL := HostedPagePane.tla
TOOLS := $(WORK)/tla2tools-1.8.0.jar
SHA := 67f9575c3b9ef7a0787b1bded34812862ffe79d711298e5a75b1a77720b19928
JAVA := java -Djava.io.tmpdir=$(WORK)/java -XX:+UseParallelGC -Xmx1g -cp $(TOOLS)
CHECKS := normal stale keep-absent clear-error remount witness
EXPECTED_stale := CurrentSelection
EXPECTED_keep-absent := LastSuccess
EXPECTED_clear-error := LastSuccess
EXPECTED_remount := NoRedundantMount
EXPECTED_witness := NeverLoadedUpdate

.PHONY: check $(CHECKS)
check: $(CHECKS)

$(TOOLS):
	mkdir -p $(WORK)
	curl -fsSL https://github.com/tlaplus/tlaplus/releases/download/v1.8.0/tla2tools.jar -o $@

$(WORK)/$(MODEL): $(MODEL) $(TOOLS)
	@mkdir -p $(WORK)/java
	@echo '$(SHA)  $(TOOLS)' | sha256sum -c -
	cp $(MODEL) $@
	cd $(WORK) && $(JAVA) pcal.trans -nocfg $(MODEL)

$(CHECKS): $(WORK)/$(MODEL)
	@cd $(WORK) && $(JAVA) tlc2.TLC -workers 1 -noGenerateSpecTE -metadir $@ -config $(CURDIR)/$@.cfg $(MODEL) > $@.log 2>&1; status=$$?; \
	if [ "$@" = normal ]; then \
		test $$status -eq 0 && grep -F 'Model checking completed. No error has been found.' $@.log || { cat $@.log; exit 1; }; \
	else \
		test $$status -eq 12 -o $$status -eq 13 && grep -F 'Error: Invariant $(EXPECTED_$@) is violated.' $@.log || { cat $@.log; exit 1; }; \
	fi
	@grep -E 'states generated|distinct states|depth of|Finished in' $(WORK)/$@.log
