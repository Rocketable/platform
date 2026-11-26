override WORK := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))../../..)/.tmp/recent-save-rejection
JAR := $(WORK)/tla2tools.jar
TLA_SHA256 := 67f9575c3b9ef7a0787b1bded34812862ffe79d711298e5a75b1a77720b19928
JAVA := java -Djava.io.tmpdir=$(WORK)/java -XX:-UsePerfData -XX:+UseParallelGC -cp $(JAR)
TLC := $(JAVA) -Xmx256m tlc2.TLC -workers 1 -noGenerateSpecTE

.NOTPARALLEL:
.PHONY: check translate fixed clear reset
check: fixed clear reset

fixed: translate
	cd "$(WORK)" && $(TLC) -metadir "$@" -config "$(CURDIR)/$@.cfg" RecentSaveRejection.tla > "$@.log" 2>&1
	grep -F 'Model checking completed. No error has been found.' "$(WORK)/$@.log"

clear: INVARIANT := NewestStaysVisible
reset: INVARIANT := SavesStayOrdered

clear reset: translate
	@cd "$(WORK)" && { status=0; $(TLC) -metadir "$@" -config "$(CURDIR)/$@.cfg" RecentSaveRejection.tla > "$@.log" 2>&1 || status=$$?; \
	{ test "$$status" -eq 12 || test "$$status" -eq 13; } && grep -F 'Invariant $(INVARIANT) is violated.' "$@.log"; }

translate: $(JAR)
	printf '%s  %s\n' "$(TLA_SHA256)" "$(JAR)" | shasum -a 256 -c -
	mkdir -p "$(WORK)/java"
	cp RecentSaveRejection.tla "$(WORK)/RecentSaveRejection.tla"
	cd "$(WORK)" && $(JAVA) pcal.trans -nocfg RecentSaveRejection.tla
	cmp "$(WORK)/RecentSaveRejection.tla" RecentSaveRejection.tla

$(JAR):
	mkdir -p "$(WORK)"
	curl -fsSL -o "$(JAR).part" https://github.com/tlaplus/tlaplus/releases/download/v1.8.0/tla2tools.jar
	printf '%s  %s\n' "$(TLA_SHA256)" "$(JAR).part" | shasum -a 256 -c -
	mv "$(JAR).part" "$(JAR)"
