# RocketClaw runs on Linux only. On a Mac, every target here runs again in a Linux container (linux.Containerfile)
# in a Colima VM of its own, LINUX_PROFILE, which shares your home folder. Its first run starts the VM and builds
# the image, which takes some minutes. Caches and the emulated shell's test image live in Docker volumes, the
# latter because the Mac's folders are case-insensitive.
ifeq ($(shell uname -s),Darwin)

LINUX_PROFILE ?= rocketclaw-linux
linux_docker := docker --context colima-$(LINUX_PROFILE)

# The command line's variables, such as GOTESTRUN='^(TestA|TestB)$$', go to the container's make as given: make
# lists them in MAKEOVERRIDES with spaces escaped and each $ doubled, so each is undone and quoted for the shell.
# \001 holds an escaped space while the list splits into words.
space := $(subst ,, )
linux_overrides = $(foreach o,$(subst \$(space),\001,$(MAKEOVERRIDES)),'$(subst \001,$(space),$(subst $$$$,$$,$(subst ','\'',$(o))))')

.DEFAULT_GOAL := run
.PHONY: linux-container
Makefile: ;

%: linux-container
	$(linux_docker) run --rm --privileged --network host -v $(HOME):$(HOME) -w $(CURDIR) \
		-v /var/run/docker.sock:/var/run/docker.sock -v rocketclaw-linux-home:/home/dev \
		-v rocketclaw-linux-sandbox:/sandbox -e SANDBOX_TEST_DIR=/sandbox \
		rocketclaw-linux make $(linux_overrides) $@

# The VM is Ubuntu, whose AppArmor lets bubblewrap make user namespaces only with a profile saying so.
linux-container:
	colima status $(LINUX_PROFILE) >/dev/null 2>&1 || colima start $(LINUX_PROFILE) --activate=false --binfmt --cpu 6 --memory 8 --disk 100 --mount $(HOME):w
	colima ssh -p $(LINUX_PROFILE) -- sudo sh -c 'printf "abi <abi/4.0>,\ninclude <tunables/global>\nprofile rocketclaw-bwrap /usr/bin/bwrap flags=(unconfined) {\n  userns,\n}\n" > /etc/apparmor.d/rocketclaw-bwrap && apparmor_parser -r /etc/apparmor.d/rocketclaw-bwrap'
	$(linux_docker) build -q -t rocketclaw-linux --build-arg GO_VERSION=$$(awk '$$1 == "go" { print $$2 }' ../../go.mod) \
		--build-arg UID=$$(id -u) - < linux.Containerfile >/dev/null

else

COVERPROFILE ?= .coverage.out
COVERPKG ?= github.com/Rocketable/platform/cmd/rocketclaw,./...
COVERAGE_REQUIRED ?= 91.8
GOTESTRUN ?= .
GO_SOURCE_CLOC_BUDGET ?= 42750
GO_SOURCE_CLOC_HAZARD_ZONE ?= 500

run:
	go run cirello.io/runner/v3@latest

vendor: .FORCE
	go mod tidy
	go mod vendor

web-assets: .FORCE
	cd web && bun install --frozen-lockfile && bun run build

oc-assets: .FORCE
	cd oc && bun install --frozen-lockfile && $(MAKE) build

generate: web-assets oc-assets vendor
	go generate ./skel ../../cmd/rocketclaw

# Moves the emulated shell's guest image to the newest Ubuntu snapshot, for both architectures (the other one
# runs under qemu-user emulation), and writes the new snapshot, locks and SBOMs when a package changed; commit
# them. `go generate ./internal/rocketclaw/sandbox` runs this.
sandbox-rootfs-update:
	go run ./sandbox/rootfs

lint: web-assets vendor
	go run -mod=readonly github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run --fix ./...

# RocketClaw is the emulated shell's guest relay inside the sandbox, so it carries no C code.
build: generate lint
	CGO_ENABLED=0 go build -o ../../bin/rocketclaw ../../cmd/rocketclaw

# The emulated shell's acceptance tests, on Linux only, in a delegated cgroup of their own. A missing
# prerequisite fails them. The guest image, built on the first run, and the test folders must be on a local
# filesystem. The image's packages and indexes stay in its packages folder, for the next build.
SANDBOX_TEST_DIR ?= $(abspath ../../.tmp)/sandbox-test
# -race needs cgo: SANDBOX_TEST_FLAGS=-race SANDBOX_TEST_CGO=1 (the binary then needs the rootfs's glibc).
# SANDBOX_TEST_SCOPE starts the tests in a delegated cgroup; CI, without a user systemd, runs them as a
# transient service under sudo, since only a service's cgroup is handed to its --uid. A shell stays there as
# the tests' parent, as `go run`'s go command does for RocketClaw.
# SANDBOX_TEST_COVERPROFILE, when set, writes the tests' coverage of COVERPKG there, for CI to merge.
# SANDBOX_TEST_PACKAGE_MIRROR, when set, is an Ubuntu mirror the image build tries first for its packages.
# The tests share one image, so the first failure, such as a build Ubuntu's archive refused, ends the run.
SANDBOX_TEST_FLAGS ?=
SANDBOX_TEST_CGO ?= 0
# In a container, such as the Linux a Mac runs these targets in, the cgroup make runs in is delegated to it
# (linux.Containerfile): its processes move into a leaf, and the tests into a fresh sibling. A RocketClaw that
# an earlier test started may already have moved make into its supervisor leaf; the scope then starts from
# that leaf's parent, as RocketClaw does on a reload.
ifneq ($(wildcard /.dockerenv),)
SANDBOX_TEST_SCOPE ?= /bin/sh -c 'cg=/sys/fs/cgroup$$(cut -d: -f3 /proc/self/cgroup); cg=$${cg%/supervisor}; mkdir -p $$cg/init $$cg/test-$$$$; for p in $$(cat $$cg/cgroup.procs); do echo $$p > $$cg/init/cgroup.procs 2>/dev/null || true; done; echo "+cpu +memory +pids" > $$cg/cgroup.subtree_control; echo $$$$ > $$cg/test-$$$$/cgroup.procs; exec "$$@"' sh
endif
SANDBOX_TEST_SCOPE ?= systemd-run --user --scope --quiet -p Delegate=yes
SANDBOX_TEST_COVERPROFILE ?=
sandbox-test:
	rm -rf $(SANDBOX_TEST_DIR)/gocover
	mkdir -p $(SANDBOX_TEST_DIR)/tmp $(SANDBOX_TEST_DIR)/gocover
	CGO_ENABLED=$(SANDBOX_TEST_CGO) go test -c $(SANDBOX_TEST_FLAGS) $(if $(SANDBOX_TEST_COVERPROFILE),-cover -covermode=atomic -coverpkg=$(COVERPKG)) -o $(SANDBOX_TEST_DIR)/harness.test ./harness
	cd harness && $(SANDBOX_TEST_SCOPE) /bin/sh -c '"$$@"; exit $$?' sh /usr/bin/env ROCKETCLAW_SANDBOX_REQUIRE=1 \
		ROCKETCLAW_SANDBOX_DIR=$(SANDBOX_TEST_DIR)/sandbox TMPDIR=$(SANDBOX_TEST_DIR)/tmp \
		$(if $(SANDBOX_TEST_PACKAGE_MIRROR),ROCKETCLAW_SANDBOX_PACKAGE_MIRROR=$(SANDBOX_TEST_PACKAGE_MIRROR)) \
		ROCKETCLAW_SANDBOX_PACKAGE_CACHE=$(SANDBOX_TEST_DIR)/packages \
		$(if $(SANDBOX_TEST_COVERPROFILE),ROCKETCLAW_SANDBOX_GOCOVERDIR=$(SANDBOX_TEST_DIR)/gocover) \
		$(SANDBOX_TEST_DIR)/harness.test -test.run 'TestEmulatedShell' -test.v -test.count 1 -test.failfast -test.timeout 30m \
		$(if $(SANDBOX_TEST_COVERPROFILE),-test.coverprofile=$(abspath $(SANDBOX_TEST_COVERPROFILE)))
# The guest relay's own coverage, which the tests copy out of each agent's folder, joins the profile.
	$(if $(SANDBOX_TEST_COVERPROFILE),go tool covdata textfmt -i=$$(ls -d $(SANDBOX_TEST_DIR)/gocover/*/ | paste -sd, -) -o $(SANDBOX_TEST_DIR)/guest.out && tail -n +2 $(SANDBOX_TEST_DIR)/guest.out >> $(abspath $(SANDBOX_TEST_COVERPROFILE)))

cloc:
	@printf "Go cloc (cmd/rocketclaw and internal/rocketclaw)\n"
	@set -eu; \
	go_source_cloc=$$(cloc --json --include-lang=Go --exclude-dir=node_modules --fullpath --not-match-f='(\.pb\.go|_test\.go)$$' ../../cmd/rocketclaw . | jq -r '.SUM.code'); \
	go_test_cloc=$$(cloc --json --include-lang=Go --exclude-dir=node_modules --fullpath --match-f='(_test\.go)$$' ../../cmd/rocketclaw . | jq -r '.SUM.code'); \
	go_total_cloc=$$(cloc --json --include-lang=Go --exclude-dir=node_modules --fullpath --not-match-f='(\.pb\.go)$$' ../../cmd/rocketclaw . | jq -r '.SUM.code'); \
	limit=$(GO_SOURCE_CLOC_BUDGET); \
	hazard_zone=$(GO_SOURCE_CLOC_HAZARD_ZONE); \
	hazard_cloc=$$((limit - hazard_zone)); \
	printf "%-18s %s\n" "source cloc:" "$$go_source_cloc"; \
	printf "%-18s %d-%d\n" "hazard zone cloc:" "$$hazard_cloc" "$$((limit - 1))"; \
	printf "%-18s %d\n" "failure cloc:" "$$limit"; \
	printf "%-18s %s\n" "test cloc:" "$$go_test_cloc"; \
	printf "%-18s %s\n" "total cloc:" "$$go_total_cloc"

test: generate lint
	$(MAKE) check-code-metrics

tests: test

# As in CI, the emulated shell's acceptance tests add their coverage, of code only they reach, to the rest's.
check-code-metrics:
	$(MAKE) coverage-profile
	$(MAKE) sandbox-test SANDBOX_TEST_COVERPROFILE=.coverage-sandbox.out
	tail -n +2 .coverage-sandbox.out >> $(COVERPROFILE)
	$(MAKE) -o coverage-profile check-coverage
	$(MAKE) check-cloc-budget

check-cloc-budget:
	@set -eu; \
	go_source_cloc=$$(cloc --json --include-lang=Go --exclude-dir=node_modules --fullpath --not-match-f='(\.pb\.go|_test\.go)$$' ../../cmd/rocketclaw . | jq -r '.SUM.code'); \
	[ -n "$$go_source_cloc" ] || { printf "source cloc budget failed: unable to read source cloc\n"; exit 1; }; \
	limit=$(GO_SOURCE_CLOC_BUDGET); \
	hazard_zone=$(GO_SOURCE_CLOC_HAZARD_ZONE); \
	lines_left=$$((limit - go_source_cloc)); \
	if [ "$$go_source_cloc" -ge "$$limit" ]; then printf "STOP IMMEDIATELY. EXECUTION MUST BE HALTED. LOSS STATE REACHED.\n\nThe limit is %d, current value %d.\n\nContinual operation under this mode can lead to more losses of features, capabilities, and deviation from Human Partner intentions.\n\nABORT!\n" "$$limit" "$$go_source_cloc"; exit 2; fi; \
	if [ "$$lines_left" -le "$$hazard_zone" ]; then printf "The total lines of source code count is inside the hazard zone. \n\nThe limit is %d, reaching this limit is considered a loss and it will cause the whole system to halt unsafely.\n\nAmong the potential losses is the undue removal of features without human approval.\n\nYou have %d lines of source code left to use. \n\nThe usage of hazard budget is tolerated but not encouraged. \n\nThink hard, preserve system attributes and original feature set to avoid product regressions.\n" "$$limit" "$$lines_left"; exit 0; fi; \

check-coverage: coverage-profile
	@awk -v required="$(COVERAGE_REQUIRED)" ' \
		NR > 1 && $$1 !~ /\.pb\.go:/ { statements[$$1] = $$2; hits[$$1] += $$3 } \
		END { \
			for (block in statements) { total += statements[block]; if (hits[block]) covered += statements[block] } \
			current = 100 * covered / total; \
			if (100 * covered < required * total) { printf "coverage budget failed: current %.6f%% (%d/%d statements) < required %g%%\n", current, covered, total, required; exit 1 } \
			printf "coverage budget ok: current %.6f%% (%d/%d statements) >= required %g%%\n", current, covered, total, required \
		}' "$(COVERPROFILE)"

# CI shards the tests with GOTESTRUN and merges the profiles.
# Test browsers whose test run was killed never exit, so they are cleared before and after the
# run. Only orphans (parent PID 1) match, so other running test runs keep their browsers. [c] keeps the
# pattern from matching this recipe's own shell, whose parent is PID 1 when make is, as in the Linux container.
coverage-profile:
	@set -eu; \
	name=; \
	cleanup() { \
		if [ -n "$$name" ]; then docker rm -f -v "$$name" >/dev/null 2>&1 || true; fi; \
		pkill -P 1 -f 'ms-playwright/[c]hromium' || true; \
	}; \
	pkill -P 1 -f 'ms-playwright/[c]hromium' || true; \
	trap cleanup EXIT INT TERM; \
	if [ -z "$${ROCKETCLAW_TEST_DATABASE_URL:-}" ]; then \
		latest=$$(curl -fsSL https://www.postgresql.org/versions.json | jq -r '[.[] | select(.supported) | .major] | max_by(tonumber)'); \
		name=rocketclaw-pg-$$$$; \
		docker run -d --name "$$name" \
			-e POSTGRES_HOST_AUTH_METHOD=trust \
			-e POSTGRES_USER=postgres \
			-e POSTGRES_PASSWORD=postgres \
			-e POSTGRES_DB=rocketclaw_test \
			-p 127.0.0.1::5432 \
			postgres:$$latest >/dev/null; \
		port=$$(docker port "$$name" 5432/tcp); \
		port=$${port##*:}; \
		i=0; \
		while [ "$$i" -lt 30 ]; do \
			if docker exec "$$name" pg_isready -U postgres -d rocketclaw_test >/dev/null 2>&1; then break; fi; \
			i=$$((i + 1)); \
			sleep 1; \
		done; \
		export ROCKETCLAW_TEST_DATABASE_URL="postgres://postgres:postgres@127.0.0.1:$$port/rocketclaw_test?sslmode=disable"; \
	fi; \
	go test -race -count 1 -covermode=atomic -coverpkg=$(COVERPKG) -coverprofile=$(COVERPROFILE) -run '$(GOTESTRUN)' github.com/Rocketable/platform/cmd/rocketclaw/... ./...

.FORCE:

deploy:
	@set -eu; \
	pid=$$(pgrep -n -x rocketclaw || true); \
	[ -n "$$pid" ] || { printf "deploy failed: no running rocketclaw process found\n"; exit 1; }; \
	target=$$(lsof -a -p "$$pid" -d txt -Fn | awk '/^n/ { print substr($$0, 2); exit }'); \
	[ -n "$$target" ] || { printf "deploy failed: unable to find running rocketclaw path for pid %s\n" "$$pid"; exit 1; }; \
	printf "deploying rocketclaw to %s (pid %s)\n" "$$target" "$$pid"; \
	$(MAKE) build; \
	mv ./bin/rocketclaw "$$target"; \
	kill "$$pid"

go-mod-update:
	go get -u ./...
	$(MAKE) vendor

sync-opencode-testdata:
	bash ./harness/scripts/sync-opencode-testdata.sh

endif
