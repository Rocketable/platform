override WORK := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))../../..)/.tmp/remote-session-offline
JAR := $(WORK)/tla2tools.jar
TLA_SHA256 := 67f9575c3b9ef7a0787b1bded34812862ffe79d711298e5a75b1a77720b19928
JAVA := java -Djava.io.tmpdir=$(WORK)/java -XX:-UsePerfData -XX:+UseParallelGC -cp $(JAR)
TLC := $(JAVA) -Xmx256m tlc2.TLC -workers 1 -noGenerateSpecTE
PASS := fixed shown-child
NEGATIVE := old guard-only cut-start hidden-child stale-readmit
EXPECTED_old := NoOfflineWithoutVisibleReport
EXPECTED_guard-only := OpenViewSurvivesOffline
EXPECTED_cut-start := KeepWarmWithOpenTabs
EXPECTED_hidden-child := OpenViewSurvivesOffline
EXPECTED_stale-readmit := NoOfflineWithoutVisibleReport

.NOTPARALLEL:
.PHONY: check translate $(PASS) $(NEGATIVE)
check: $(PASS) $(NEGATIVE)

$(PASS): translate
	cd "$(WORK)" && $(TLC) -metadir "$@" -config "$(CURDIR)/$@.cfg" OfflineView.tla > "$@.log" 2>&1
	grep -F 'Model checking completed. No error has been found.' "$(WORK)/$@.log"

$(NEGATIVE): translate
	@cd "$(WORK)" && { status=0; $(TLC) -metadir "$@" -config "$(CURDIR)/$@.cfg" OfflineView.tla > "$@.log" 2>&1 || status=$$?; \
	{ test "$$status" -eq 12 || test "$$status" -eq 13; } && grep -F 'Invariant $(EXPECTED_$@) is violated.' "$@.log"; }

translate: $(JAR)
	printf '%s  %s\n' "$(TLA_SHA256)" "$(JAR)" | shasum -a 256 -c -
	mkdir -p "$(WORK)/java"
	cp OfflineView.tla "$(WORK)/OfflineView.tla"
	cd "$(WORK)" && $(JAVA) pcal.trans -nocfg OfflineView.tla
	cmp "$(WORK)/OfflineView.tla" OfflineView.tla

$(JAR):
	mkdir -p "$(WORK)"
	curl -fsSL -o "$(JAR).part" https://github.com/tlaplus/tlaplus/releases/download/v1.8.0/tla2tools.jar
	printf '%s  %s\n' "$(TLA_SHA256)" "$(JAR).part" | shasum -a 256 -c -
	mv "$(JAR).part" "$(JAR)"
